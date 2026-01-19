# Keystone: Why Performance and Simplicity Matter at the Edge

In the world of Internet of Things (IoT) and Edge Computing, managing software across thousands of remote devices is a daunting challenge. For years, **AWS IoT Greengrass** has been the go-to solution for many. However, as edge fleets grow and hardware becomes more diverse, many developers are hitting the "Java tax"—the high resource overhead and complexity that comes with heavy, cloud-first runtimes.

Enter **Keystone**: a lightweight, robust, and secure edge orchestration agent written in Go.

## The Edge Challenge: The "Java Tax"

AWS Greengrass is powerful, but it comes with a significant footprint. Running a Java Virtual Machine (JVM) on a resource-constrained ARM device or a small industrial PC consumes precious RAM and CPU cycles before your actual application even starts.

Keystone was born from a simple observation: edge orchestration shouldn't be heavier than the applications it manages.

## What is Keystone?

Keystone is a minimal yet robust orchestrator designed to keep edge devices converging to a desired state—even with flaky network connections. It is written in **Go**, which means it compiles to a single, statically-linked binary with no external dependencies like a JVM or a full Docker daemon.

### Key Highlights

- **Ultra-lightweight**: Idle CPU ~0%, and a RAM baseline of less than 40MB.
- **Process-First**: Orchestrates native processes by default, providing maximum performance and hardware access.
- **Predictable**: Uses atomic deployments, checkpoints, and reliable rollbacks.
- **Secure**: Built with mTLS support, artifact signing, and the principle of least privilege.

## Keystone vs. AWS Greengrass: A Pragmatic Comparison

| Feature              | AWS Greengrass (v2)                | Keystone                             |
| :------------------- | :--------------------------------- | :----------------------------------- |
| **Runtime**          | Java (JVM) or C (Lite)             | Go (Native)                          |
| **Memory Footprint** | ~100MB+ (Java)                     | **< 40MB**                           |
| **Configuration**    | Complex JSON/YAML Recipes          | **Simple TOML Recipes**              |
| **Deployment**       | Cloud-orchestrated (IoT Jobs)      | Local-first, Cloud-agnostic          |
| **Execution**        | Components as Processes/Containers | Processes-first, Containers optional |

Keystone embraces the "processes first, containers when needed" philosophy. While Greengrass often feels like it's dragging the cloud to the edge, Keystone feels like a natural extension of the edge hardware itself.

## The Keystone Toolkit

Keystone provides a simple, two-tool approach to edge management:

1. **`keystone`**: The core agent. It runs as a background service (e.g., via systemd), monitors component health, manages artifact downloads, and enforces the desired state.
2. **`keystonectl`**: A developer-friendly CLI for interacting with the local agent. You can check status, restart components, or dry-run new deployment plans with ease.

## Simple and Powerful Usage Examples

One of Keystone's greatest strengths is its simplicity. Let's look at how easy it is to manage services.

### 1. Installing a Service

To deploy a service, you define a **Deployment Plan** (a simple list of components) and a **Recipe** (which describes how to run the component).

**Example Recipe (`hello.recipe.toml`):**

```toml
[metadata]
name = "com.example.hello"
version = "1.0.0"
type = "generic"

[[artifacts]]
uri = "https://example.com/artifacts/hello-v1.0.0.tar.gz"
sha256 = "..."
unpack = true

[lifecycle.run.exec]
command = "./hello"
args = ["--port", "8080"]
```

Applying this plan is a single command:

```bash
./keystonectl apply plan.toml
```

### 2. Updating with Confidence

Updating a service in Keystone is atomic. If a new version fails its health check, Keystone can automatically roll back to the previous known-good state.

You can even **dry-run** an update to see the impact before committing:

```bash
./keystonectl apply-dry new-plan.toml
```

### 3. Monitoring Health

Check the status of all your components instantly:

```bash
./keystonectl status
```

This gives you a clear overview of which services are running, their uptime, and their current health status.

## Conclusion: Leaner is Better

For many edge use cases, the full weight of AWS Greengrass is overkill. Keystone offers a pragmatic, high-performance alternative that respects your hardware's resources while providing the security and reliability required for production fleets.

If you are looking for a way to orchestrate your edge without the "Java tax" or the complexity of a cloud-first monolith, it's time to try Keystone.

---
*Keystone is open-source and looking for contributors who care about reliability at the edge. Check out our [README](README.md) to get started!*
