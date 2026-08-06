package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type call struct {
	Args          []string `json:"args"`
	Home          string   `json:"home"`
	APIKey        string   `json:"api_key"`
	Server        string   `json:"server"`
	RegionCode    string   `json:"region_code"`
	TDCPublicKey  string   `json:"tdc_public_key,omitempty"`
	TDCPrivateKey string   `json:"tdc_private_key,omitempty"`
	TDCFSToken    string   `json:"tdc_fs_token,omitempty"`
	Drive9Public  string   `json:"drive9_public_key,omitempty"`
	Drive9Private string   `json:"drive9_private_key,omitempty"`
}

type tenant struct {
	TenantID string `json:"tenant_id"`
	Status   string `json:"status"`
	Kind     string `json:"kind"`
}

func main() {
	record := os.Getenv("FAKE_DRIVE9_RECORD")
	if record != "" {
		file, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		_ = json.NewEncoder(file).Encode(call{
			Args:          os.Args[1:],
			Home:          os.Getenv("HOME"),
			APIKey:        os.Getenv("DRIVE9_API_KEY"),
			Server:        os.Getenv("DRIVE9_SERVER"),
			RegionCode:    os.Getenv("DRIVE9_REGION_CODE"),
			TDCPublicKey:  os.Getenv("TDC_PUBLIC_KEY"),
			TDCPrivateKey: os.Getenv("TDC_PRIVATE_KEY"),
			TDCFSToken:    os.Getenv("TDC_FS_TOKEN"),
			Drive9Public:  os.Getenv("DRIVE9_PUBLIC_KEY"),
			Drive9Private: os.Getenv("DRIVE9_PRIVATE_KEY"),
		})
		_ = file.Close()
	}
	args := os.Args[1:]
	if hasPrefix(args, "create") {
		region := flagValue(args, "--region-code")
		id := "tenant-" + strings.ReplaceAll(region, "_", "-")
		state := loadState()
		state[id] = tenant{TenantID: id, Status: "active", Kind: "tidb_cloud"}
		saveState(state)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"tenant_id":      id,
			"api_key":        tokenFor(id),
			"status":         "provisioned",
			"cloud_provider": "aws",
			"region_code":    region,
		})
		return
	}
	if hasPrefix(args, "admin", "tenant", "list") {
		state := loadState()
		tenants := make([]tenant, 0, len(state))
		for _, item := range state {
			tenants = append(tenants, item)
		}
		sort.Slice(tenants, func(i, j int) bool { return tenants[i].TenantID < tenants[j].TenantID })
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"tenants": tenants, "page": 1, "page_size": 100, "next_page": 0})
		return
	}
	if hasPrefix(args, "admin", "tenant", "get") {
		id := flagValue(args, "--tenant-id")
		item, ok := loadState()[id]
		if !ok {
			fmt.Fprintln(os.Stderr, "tenant not found")
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(item)
		return
	}
	if hasPrefix(args, "admin", "tenant", "delete") {
		state := loadState()
		delete(state, flagValue(args, "--tenant-id"))
		saveState(state)
		fmt.Println(`{"status":"deleting"}`)
		return
	}
	if len(args) >= 2 && args[0] == "vault" && args[1] == "ls" {
		fmt.Println(`{"secrets":[]}`)
		return
	}
	if len(args) >= 2 && args[0] == "journal" && args[1] == "new" {
		fmt.Println(`{}`)
		return
	}
	if len(args) >= 2 && args[0] == "fs" && args[1] == "stat" {
		if expected := os.Getenv("FAKE_DRIVE9_EXPECT_API_KEY"); expected != "" && os.Getenv("DRIVE9_API_KEY") != expected {
			fmt.Fprintln(os.Stderr, "fs stat: unauthorized")
			os.Exit(1)
		}
		fmt.Println(`{"path":"/","size":0,"isdir":true}`)
	}
}

func hasPrefix(args []string, want ...string) bool {
	if len(args) < len(want) {
		return false
	}
	for i := range want {
		if args[i] != want[i] {
			return false
		}
	}
	return true
}

func loadState() map[string]tenant {
	state := map[string]tenant{}
	data, err := os.ReadFile(os.Getenv("FAKE_DRIVE9_STATE"))
	if err == nil {
		_ = json.Unmarshal(data, &state)
	}
	return state
}

func saveState(state map[string]tenant) {
	path := os.Getenv("FAKE_DRIVE9_STATE")
	if path == "" {
		return
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		panic(err)
	}
}

func tokenFor(id string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]string{"tenant_id": id})
	jwt := header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	return "drive9_" + base64.RawURLEncoding.EncodeToString([]byte(jwt))
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
