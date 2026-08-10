package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

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

	if opts.Query != "" {
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
		return renderJSON(w, value)
	}
}
