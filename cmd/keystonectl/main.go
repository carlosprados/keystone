package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/carlosprados/keystone/internal/version"
)

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8080", "Keystone agent base URL")
	flag.Parse()
	if flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}
	cmd := flag.Arg(0)
	switch cmd {
	case "version":
		fmt.Printf("keystonectl version %s (commit %s)\n", version.Version, version.Commit)
	case "status":
		doGET(*addr + "/v1/plan/status")
	case "components":
		doGET(*addr + "/v1/components")
	case "stop-plan":
		doPOST(*addr + "/v1/plan/stop")
	case "apply-dry":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "missing plan path")
			os.Exit(2)
		}
		plan := flag.Arg(1)
		doPOSTJSON(*addr+"/v1/plan/apply", map[string]any{"planPath": plan, "dry": true})
	case "apply":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "missing plan path")
			os.Exit(2)
		}
		path := flag.Arg(1)
		dry := false
		if flag.NArg() > 2 && flag.Arg(2) == "--dry" {
			dry = true
		}
		doUploadPlan(*addr+"/v1/plan/apply", path, dry)
	case "graph":
		doGET(*addr + "/v1/plan/graph")
	case "restart-dry":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "missing component name")
			os.Exit(2)
		}
		name := flag.Arg(1)
		doPOST(*addr + "/v1/components/" + strings.Trim(name, "/") + ":restart?dry=true")
	case "stop":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "missing component name")
			os.Exit(2)
		}
		name := flag.Arg(1)
		doPOST(*addr + "/v1/components/" + strings.Trim(name, "/") + ":stop")
	case "restart":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "missing component name")
			os.Exit(2)
		}
		name := flag.Arg(1)
		doPOST(*addr + "/v1/components/" + strings.Trim(name, "/") + ":restart")
	case "upload-recipe":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "missing recipe path")
			os.Exit(2)
		}
		path := flag.Arg(1)
		force := false
		if flag.NArg() > 2 && flag.Arg(2) == "--force" {
			force = true
		}
		doUploadRecipe(*addr+"/v1/recipes", path, force)
	case "sha256":
		if flag.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "missing file path")
			os.Exit(2)
		}
		path := flag.Arg(1)
		doSHA256(path)
	case "recipes":
		doGET(*addr + "/v1/recipes")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("keystonectl [--addr URL] <command> [args]")
	fmt.Println("commands:")
	fmt.Println("  version                  Show version")
	fmt.Println("  status                   Show plan status")
	fmt.Println("  components               List components")
	fmt.Println("  stop-plan                Stop all components")
	fmt.Println("  apply <plan> [--dry]     Apply plan remotely")
	fmt.Println("  apply-dry <plan>         Apply plan (dry-run) and show order")
	fmt.Println("  stop <name>              Stop a component")
	fmt.Println("  restart <name>           Restart a component")
	fmt.Println("  restart-dry <name>       Show restart order (dry-run)")
	fmt.Println("  graph                    Show plan graph and order")
	fmt.Println("  upload-recipe <path>     Upload a recipe to the agent")
	fmt.Println("  recipes                  List uploaded recipes")
	fmt.Println("  sha256 <path>            Calculate SHA256 hash of a file")
}

func doGET(url string) {
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		io.Copy(os.Stderr, resp.Body)
		os.Exit(1)
	}
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		// print raw
		resp.Body.Close()
		fmt.Println()
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	os.Stdout.Write(b)
	fmt.Println()
}

func doPOST(url string) {
	req, _ := http.NewRequest("POST", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		io.Copy(os.Stderr, resp.Body)
		os.Exit(1)
	}
	fmt.Println("OK")
}

func doPOSTJSON(url string, body any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		io.Copy(os.Stderr, resp.Body)
		os.Exit(1)
	}
	fmt.Println("OK")
}

func doUploadRecipe(addr, path string, force bool) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	u := addr
	if force {
		u += "?force=true"
	}

	req, _ := http.NewRequest("POST", u, f)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		io.Copy(os.Stderr, resp.Body)
		fmt.Println()
		os.Exit(1)
	}
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
}

func doSHA256(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(hex.EncodeToString(h.Sum(nil)))
}

func doUploadPlan(addr, path string, dry bool) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	u := addr
	if dry {
		u += "?dry=true"
	}

	req, _ := http.NewRequest("POST", u, f)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		io.Copy(os.Stderr, resp.Body)
		fmt.Println()
		os.Exit(1)
	}
	fmt.Println("OK")
}
