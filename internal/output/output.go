package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/query"
)

const (
	FormatJSON = "json"
	FormatText = "text"
)

type Options struct {
	Format string
	Query  string
}

type Humaner interface {
	Human() string
}

type Raw struct {
	Bytes []byte
}

// Render applies query options and writes a result in the requested output mode.
func Render(w io.Writer, value any, opts Options) error {
	format := opts.Format
	if format == "" {
		format = FormatJSON
	}
	if format != FormatJSON && format != FormatText {
		return apperr.New(
			"output.invalid_format",
			"usage",
			2,
			fmt.Sprintf("unsupported --output %q; supported values: json, text", format),
		)
	}

	if raw, ok := value.(Raw); ok {
		if opts.Query != "" {
			return apperr.New(
				"output.query_not_supported",
				"usage",
				2,
				"--query requires structured output; rerun without --query for raw output commands",
			)
		}
		_, err := w.Write(raw.Bytes)
		return err
	}

	queried := opts.Query != ""
	if queried {
		result, err := query.Apply(opts.Query, value)
		if err != nil {
			return apperr.Wrap(
				"output.invalid_query",
				"usage",
				2,
				fmt.Sprintf("invalid --query expression %q; check the JMESPath expression and try again", opts.Query),
				err,
			)
		}
		value = result
	}

	switch format {
	case FormatJSON:
		return renderJSON(w, value)
	case FormatText:
		if queried {
			return renderQueryText(w, value)
		}
		return renderHuman(w, value)
	default:
		panic("unreachable output format")
	}
}

func renderJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return apperr.Wrap("output.render_json", "runtime", 1, "render JSON output", err)
	}
	return nil
}

func renderHuman(w io.Writer, value any) error {
	if human, ok := value.(Humaner); ok {
		text := strings.TrimRight(human.Human(), "\n")
		if text == "" {
			return nil
		}
		_, err := fmt.Fprintln(w, text)
		return err
	}

	switch typed := value.(type) {
	case nil:
		_, err := fmt.Fprintln(w, "null")
		return err
	case string:
		_, err := fmt.Fprintln(w, typed)
		return err
	case bool, float64, int, int64, uint64:
		_, err := fmt.Fprintln(w, typed)
		return err
	default:
		return apperr.New(
			"output.text_formatter_missing",
			"runtime",
			1,
			fmt.Sprintf("text output is not implemented for result type %T", value),
		)
	}
}

func renderQueryText(w io.Writer, value any) error {
	switch typed := value.(type) {
	case nil:
		_, err := fmt.Fprintln(w, "null")
		return err
	case string, bool, float64:
		_, err := fmt.Fprintln(w, textCell(typed))
		return err
	case []any:
		return renderQueryList(w, typed)
	case map[string]any:
		return renderQueryObject(w, typed)
	default:
		return apperr.New("output.query_text_type", "runtime", 1, fmt.Sprintf("cannot render queried result type %T as text", value))
	}
}

func renderQueryList(w io.Writer, values []any) error {
	if len(values) == 0 {
		return nil
	}
	objects := make([]map[string]any, 0, len(values))
	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			for _, item := range values {
				if _, err := fmt.Fprintln(w, textCell(item)); err != nil {
					return err
				}
			}
			return nil
		}
		objects = append(objects, object)
	}
	keys := queryObjectKeys(objects...)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(keys, "\t")); err != nil {
		return err
	}
	for _, object := range objects {
		cells := make([]string, 0, len(keys))
		for _, key := range keys {
			cells = append(cells, textCell(object[key]))
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func renderQueryObject(w io.Writer, object map[string]any) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, key := range queryObjectKeys(object) {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", key, textCell(object[key])); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func queryObjectKeys(objects ...map[string]any) []string {
	seen := make(map[string]struct{})
	for _, object := range objects {
		for key := range object {
			seen[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func textCell(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return fmt.Sprint(typed)
	case float64:
		return fmt.Sprint(typed)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(encoded)
	}
}
