package sqlresult

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
)

type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

type Result struct {
	Fields       []Field            `json:"fields"`
	Rows         []map[string]any   `json:"rows"`
	RowCount     int                `json:"row_count"`
	RowsAffected *int64             `json:"rows_affected,omitempty"`
	LastInsertID *string            `json:"last_insert_id,omitempty"`
	Transport    string             `json:"transport"`
	AccessMode   sqlcred.AccessMode `json:"access_mode"`
	ClusterID    string             `json:"cluster_id"`
	Session      string             `json:"session,omitempty"`
}

func (r Result) Human() string {
	fields := r.Fields
	rows := r.Rows
	if len(fields) == 0 {
		fields = fieldsFromRows(rows)
	}
	if len(fields) == 0 {
		return queryOK(r.RowsAffected, r.LastInsertID)
	}

	rowValues := make([][]string, 0, len(rows))
	widths := make([]int, len(fields))
	for i, field := range fields {
		name := field.Name
		if name == "" {
			name = "c" + strconv.Itoa(i)
			fields[i].Name = name
		}
		widths[i] = displayWidth(name)
	}

	for _, row := range rows {
		values := make([]string, len(fields))
		for i, field := range fields {
			value := displayValue(row[field.Name])
			values[i] = value
			if width := displayWidth(value); width > widths[i] {
				widths[i] = width
			}
		}
		rowValues = append(rowValues, values)
	}

	var out strings.Builder
	separator := tableSeparator(widths)
	out.WriteString(separator)
	out.WriteByte('\n')
	writeTableRow(&out, fieldNames(fields), widths)
	out.WriteString(separator)
	out.WriteByte('\n')
	for _, values := range rowValues {
		writeTableRow(&out, values, widths)
	}
	out.WriteString(separator)
	out.WriteByte('\n')

	rowCount := r.RowCount
	if rowCount == 0 && len(rows) > 0 {
		rowCount = len(rows)
	}
	out.WriteString(fmt.Sprintf("%d %s in set", rowCount, rowWord(rowCount)))
	return out.String()
}

func DecodeRows(fields []Field, rawRows [][]*string) []map[string]any {
	rows := make([]map[string]any, 0, len(rawRows))
	for _, rawRow := range rawRows {
		row := map[string]any{}
		for i, value := range rawRow {
			field := Field{Name: "c" + strconv.Itoa(i)}
			if i < len(fields) {
				field = fields[i]
				if field.Name == "" {
					field.Name = "c" + strconv.Itoa(i)
				}
			}
			if value == nil {
				row[field.Name] = nil
				continue
			}
			row[field.Name] = Cast(field.Type, *value)
		}
		rows = append(rows, row)
	}
	return rows
}

func Cast(fieldType, value string) any {
	normalized := strings.ToUpper(strings.TrimSpace(fieldType))
	switch normalized {
	case "TINYINT", "UNSIGNED TINYINT", "SMALLINT", "UNSIGNED SMALLINT", "MEDIUMINT", "UNSIGNED MEDIUMINT", "INT", "UNSIGNED INT", "YEAR":
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	case "FLOAT", "DOUBLE":
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	case "JSON":
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err == nil {
			return decoded
		}
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "BIT":
		if decoded, err := hex.DecodeString(value); err == nil {
			return decoded
		}
	}
	return value
}

func fieldsFromRows(rows []map[string]any) []Field {
	seen := map[string]struct{}{}
	var names []string
	for _, row := range rows {
		for name := range row {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	fields := make([]Field, 0, len(names))
	for _, name := range names {
		fields = append(fields, Field{Name: name})
	}
	return fields
}

func fieldNames(fields []Field) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = field.Name
	}
	return names
}

func tableSeparator(widths []int) string {
	var out strings.Builder
	out.WriteByte('+')
	for _, width := range widths {
		out.WriteString(strings.Repeat("-", width+2))
		out.WriteByte('+')
	}
	return out.String()
}

func writeTableRow(out *strings.Builder, values []string, widths []int) {
	out.WriteByte('|')
	for i, value := range values {
		out.WriteByte(' ')
		out.WriteString(value)
		out.WriteString(strings.Repeat(" ", widths[i]-displayWidth(value)))
		out.WriteByte(' ')
		out.WriteByte('|')
	}
	out.WriteByte('\n')
}

func displayValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NULL"
	case []byte:
		return "0x" + hex.EncodeToString(typed)
	case string:
		return cleanCell(typed)
	case map[string]any:
		return displayJSON(typed)
	case []any:
		return displayJSON(typed)
	default:
		return cleanCell(fmt.Sprint(typed))
	}
}

func displayJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return cleanCell(fmt.Sprint(value))
	}
	return cleanCell(string(data))
}

func cleanCell(value string) string {
	value = strings.ReplaceAll(value, "\r", `\r`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\t", `\t`)
	return value
}

func displayWidth(value string) int {
	return len([]rune(value))
}

func queryOK(rowsAffected *int64, lastInsertID *string) string {
	if rowsAffected == nil {
		return "Query OK, 0 rows affected"
	}
	message := fmt.Sprintf("Query OK, %d %s affected", *rowsAffected, rowWord(int(*rowsAffected)))
	if lastInsertID != nil && *lastInsertID != "" {
		message += ", last insert id: " + *lastInsertID
	}
	return message
}

func rowWord(count int) string {
	if count == 1 {
		return "row"
	}
	return "rows"
}
