package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "net/http"
    "os"
    "strings"
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
    case "status":
        doGET(*addr+"/v1/plan/status")
    case "components":
        doGET(*addr+"/v1/components")
    case "stop-plan":
        doPOST(*addr+"/v1/plan/stop")
    case "apply-dry":
        if flag.NArg() < 2 { fmt.Fprintln(os.Stderr, "missing plan path"); os.Exit(2) }
        plan := flag.Arg(1)
        doPOSTJSON(*addr+"/v1/plan/apply", map[string]any{"planPath": plan, "dry": true})
    case "graph":
        doGET(*addr+"/v1/plan/graph")
    case "restart-dry":
        if flag.NArg() < 2 { fmt.Fprintln(os.Stderr, "missing component name"); os.Exit(2) }
        name := flag.Arg(1)
        doPOST(*addr+"/v1/components/"+strings.Trim(name, "/")+":restart?dry=true")
    case "stop":
        if flag.NArg() < 2 { fmt.Fprintln(os.Stderr, "missing component name"); os.Exit(2) }
        name := flag.Arg(1)
        doPOST(*addr+"/v1/components/"+strings.Trim(name, "/")+":stop")
    case "restart":
        if flag.NArg() < 2 { fmt.Fprintln(os.Stderr, "missing component name"); os.Exit(2) }
        name := flag.Arg(1)
        doPOST(*addr+"/v1/components/"+strings.Trim(name, "/")+":restart")
    default:
        usage()
        os.Exit(2)
    }
}

func usage() {
    fmt.Println("keystonectl [--addr URL] <command> [args]")
    fmt.Println("commands:")
    fmt.Println("  status                   Show plan status")
    fmt.Println("  components               List components")
    fmt.Println("  stop-plan                Stop all components")
    fmt.Println("  apply-dry <plan>         Apply plan (dry-run) and show order")
    fmt.Println("  stop <name>              Stop a component")
    fmt.Println("  restart <name>           Restart a component")
    fmt.Println("  restart-dry <name>       Show restart order (dry-run)")
    fmt.Println("  graph                    Show plan graph and order")
}

func doGET(url string) {
    resp, err := http.Get(url)
    if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 { io.Copy(os.Stderr, resp.Body); os.Exit(1) }
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
    if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 { io.Copy(os.Stderr, resp.Body); os.Exit(1) }
    fmt.Println("OK")
}

func doPOSTJSON(url string, body any) {
    b, _ := json.Marshal(body)
    req, _ := http.NewRequest("POST", url, strings.NewReader(string(b)))
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req)
    if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 { io.Copy(os.Stderr, resp.Body); os.Exit(1) }
    fmt.Println("OK")
}
