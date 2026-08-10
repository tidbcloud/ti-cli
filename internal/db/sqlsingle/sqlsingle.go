package sqlsingle

import (
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func Validate(statement string) error {
	if strings.TrimSpace(statement) == "" {
		return apperr.New("db.sql_missing", "usage", 2, "--sql is required")
	}
	if !isSingle(statement) {
		return apperr.New("db.sql_multiple_statements", "usage", 2, "execute-sql-statement accepts exactly one SQL statement")
	}
	return nil
}

func isSingle(statement string) bool {
	inSingle := false
	inDouble := false
	inBacktick := false
	escaped := false
	seenTerminator := false
	for i := 0; i < len(statement); i++ {
		ch := statement[i]
		if escaped {
			escaped = false
			continue
		}
		if inSingle || inDouble {
			if ch == '\\' {
				escaped = true
				continue
			}
		}
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			}
		case inBacktick:
			if ch == '`' {
				inBacktick = false
			}
		default:
			switch ch {
			case '\'':
				if seenTerminator && strings.TrimSpace(statement[i:]) != "" {
					return false
				}
				inSingle = true
			case '"':
				if seenTerminator && strings.TrimSpace(statement[i:]) != "" {
					return false
				}
				inDouble = true
			case '`':
				if seenTerminator && strings.TrimSpace(statement[i:]) != "" {
					return false
				}
				inBacktick = true
			case ';':
				if seenTerminator {
					return false
				}
				seenTerminator = true
			default:
				if seenTerminator && !isSpace(ch) {
					return false
				}
			}
		}
	}
	return !inSingle && !inDouble && !inBacktick
}

func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}
