package connectionstring

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
)

const (
	FormatMySQLURI    = "mysql-uri"
	FormatJDBC        = "jdbc"
	FormatGoSQLDriver = "go-sql-driver"
	FormatSQLAlchemy  = "sqlalchemy"
	FormatEnv         = "env"

	TLSModeVerifyIdentity = "VERIFY_IDENTITY"
)

type Input struct {
	ClusterID              string
	AccessMode             sqlcred.AccessMode
	Username               string
	Password               string
	Host                   string
	Port                   int32
	Database               string
	Format                 string
	EnvPrefix              string
	EnvIncludeDatabaseURL  bool
	EnvDatabaseURLVariable string
}

type Result struct {
	ClusterID        string             `json:"cluster_id"`
	AccessMode       sqlcred.AccessMode `json:"access_mode"`
	Username         string             `json:"username"`
	Host             string             `json:"host"`
	Port             int32              `json:"port"`
	Database         string             `json:"database,omitempty"`
	TLSMode          string             `json:"tls_mode"`
	Format           string             `json:"format"`
	ConnectionString string             `json:"connection_string"`
}

func (r Result) Human() string {
	return r.ConnectionString
}

func Build(input Input) (Result, error) {
	format := input.Format
	if format == "" {
		format = FormatMySQLURI
	}
	if err := ValidateFormat(format); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(input.Host) == "" {
		return Result{}, apperr.New("db.connection_missing_host", "api", 1, "cluster public endpoint host is missing; describe the cluster and try again after it becomes ACTIVE")
	}
	if input.Port <= 0 {
		return Result{}, apperr.New("db.connection_missing_port", "api", 1, "cluster public endpoint port is missing; describe the cluster and try again after it becomes ACTIVE")
	}

	connectionString := ""
	switch format {
	case FormatMySQLURI:
		connectionString = mysqlURI(input)
	case FormatJDBC:
		connectionString = jdbc(input)
	case FormatGoSQLDriver:
		connectionString = goSQLDriver(input)
	case FormatSQLAlchemy:
		connectionString = sqlalchemy(input)
	case FormatEnv:
		connectionString = Env(input)
	default:
		panic("unreachable connection string format")
	}

	return Result{
		ClusterID:        input.ClusterID,
		AccessMode:       input.AccessMode,
		Username:         input.Username,
		Host:             input.Host,
		Port:             input.Port,
		Database:         input.Database,
		TLSMode:          TLSModeVerifyIdentity,
		Format:           format,
		ConnectionString: connectionString,
	}, nil
}

func ValidateFormat(format string) error {
	switch format {
	case FormatMySQLURI, FormatJDBC, FormatGoSQLDriver, FormatSQLAlchemy, FormatEnv:
		return nil
	default:
		return apperr.New(
			"db.invalid_connection_string_format",
			"usage",
			2,
			"--format must be mysql-uri, jdbc, go-sql-driver, sqlalchemy, or env",
		)
	}
}

func Env(input Input) string {
	prefix := input.EnvPrefix
	if prefix == "" {
		prefix = "TIDB_"
	}
	databaseURLName := input.EnvDatabaseURLVariable
	if databaseURLName == "" {
		databaseURLName = "DATABASE_URL"
	}
	lines := map[string]string{
		prefix + "HOST":              input.Host,
		prefix + "PORT":              strconv.FormatInt(int64(input.Port), 10),
		prefix + "USER":              input.Username,
		prefix + "PASSWORD":          input.Password,
		prefix + "DATABASE":          input.Database,
		prefix + "SSL_MODE":          TLSModeVerifyIdentity,
		prefix + "ACCESS_MODE":       string(input.AccessMode),
		prefix + "CONNECTION_FORMAT": FormatEnv,
	}
	if input.EnvIncludeDatabaseURL {
		lines[databaseURLName] = mysqlURI(input)
	}
	keys := make([]string, 0, len(lines))
	for key := range lines {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(dotenvValue(lines[key]))
		out.WriteByte('\n')
	}
	return out.String()
}

func mysqlURI(input Input) string {
	u := url.URL{
		Scheme: "mysql",
		User:   url.UserPassword(input.Username, input.Password),
		Host:   net.JoinHostPort(input.Host, strconv.FormatInt(int64(input.Port), 10)),
	}
	if input.Database != "" {
		u.Path = "/" + input.Database
	}
	query := url.Values{}
	query.Set("ssl-mode", TLSModeVerifyIdentity)
	u.RawQuery = query.Encode()
	return u.String()
}

func jdbc(input Input) string {
	query := url.Values{}
	query.Set("user", input.Username)
	query.Set("password", input.Password)
	query.Set("sslMode", TLSModeVerifyIdentity)
	path := ""
	if input.Database != "" {
		path = "/" + pathEscape(input.Database)
	}
	return fmt.Sprintf("jdbc:mysql://%s%s?%s", net.JoinHostPort(input.Host, strconv.FormatInt(int64(input.Port), 10)), path, query.Encode())
}

func goSQLDriver(input Input) string {
	query := url.Values{}
	query.Set("tls", "true")
	query.Set("parseTime", "true")
	database := ""
	if input.Database != "" {
		database = pathEscape(input.Database)
	}
	return fmt.Sprintf("%s:%s@tcp(%s)/%s?%s", escapeDSNUserInfo(input.Username), escapeDSNUserInfo(input.Password), net.JoinHostPort(input.Host, strconv.FormatInt(int64(input.Port), 10)), database, query.Encode())
}

func sqlalchemy(input Input) string {
	u := url.URL{
		Scheme: "mysql+pymysql",
		User:   url.UserPassword(input.Username, input.Password),
		Host:   net.JoinHostPort(input.Host, strconv.FormatInt(int64(input.Port), 10)),
	}
	if input.Database != "" {
		u.Path = "/" + input.Database
	}
	query := url.Values{}
	query.Set("ssl_verify_identity", "true")
	u.RawQuery = query.Encode()
	return u.String()
}

func pathEscape(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), "+", "%20")
}

func escapeDSNUserInfo(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		":", "\\:",
		"@", "\\@",
		"/", "\\/",
	)
	return replacer.Replace(value)
}

func dotenvValue(value string) string {
	if value == "" {
		return `""`
	}
	needsQuote := false
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '#' || r == '"' || r == '\'' || r == '$' || r == '`' || r == '\\' || r == '\n' || r == '\r' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return value
	}
	escaped := strings.NewReplacer(
		"\\", "\\\\",
		"\n", "\\n",
		"\r", "\\r",
		"\"", "\\\"",
	).Replace(value)
	return `"` + escaped + `"`
}
