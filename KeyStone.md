[[AWS GreenGrass]]

## Intro

KeyStone es un clon de GreenGrass en Go **ligero y sencillo**, pero capaz de **gestionar software en el edge** con garantías, a continuación se ofrece una propuesta de arquitectura y plan de entrega para **KeyStone** (un “Greengrass-like” en Go, con procesos nativos por defecto y contenedores como opción).

## Objetivos de diseño

- **Ligero**: baseline < 30–40 MB RAM, CPU en reposo ~0 %.
- **Sólido**: despliegues atómicos, rollback fiable, operación offline-first.
- **Seguro**: mTLS, firma de artefactos, principio de mínimo privilegio.
- **Portátil**: Linux x86/ARM; sin dependencia obligatoria de Docker/CRI.
- **Extensible**: runners y “adapters” de control-plane enchufables.
- **Extensible**: runners y “adapters” de control-plane enchufables.
- **Operable**: métricas Prometheus, logs estructurados, trazas opcionales.

---

## Arquitectura lógica

**1) Keystone Agent (núcleo)**
Servicio único (systemd opcional) escrito en Go. Responsabilidades:

- **Supervisor** de componentes (FSM de ciclo de vida).
- **Deployment engine** (planificador, DAG de dependencias, rollback).
- **Artifact manager** (descarga, verificación, caché y GC).
- **Config manager** (capas: base → dispositivo → despliegue).
- **Comms adapters** (MQTT, HTTP-pull, NATS… enchufables).
- **Policy & Secrets** (x509, JWT opcional, KMS/HSM/TPM si aplica).
- **Observabilidad** (logs, métricas, health, eventos).

**2) Runners (plugins de ejecución)**
- **ProcessRunner (por defecto)**: lanza procesos nativos con:
	- `setuid/setgid` a usuario/grupo dedicados (`ks_user:ks_group`).
	- **cgroups v2** para CPU/mem (si disponibles).
	- **seccomp/AppArmor** (perfiles predefinidos).
	- `ulimit`, `nice/ionice`, namespaces opcionales (user/pid/net).
- **ContainerRunner (opcional)**:
	- Backend **containerd** (vía CRI) o **nerdctl**; alternativa Docker.
	- Soporte rootless cuando sea posible.
	- Mapeo controlado de `/dev` y capacidades.

**3) Control Plane Adapters**
- **MQTT adapter**: mTLS, topics `keystone/{deviceId}/jobs|state|logs|metrics`.
- **HTTP/REST adapter**: *long-polling* o SSE en redes con proxies duros.
- **WiseWolf adapter** (futuro): mapeo de Jobs/Deployments de tu plataforma.
- **OpenGate adapter** (futuro): mapeo de Jobs/Deployments de tu plataforma.

**4) Almacenamiento local**
- **State store**: SQLite o BadgerDB (embebido) para:
	- Estado de componentes y jobs.
	- Índice de artefactos.
	- Checkpoints de despliegue (para resumir tras reboot).
- **FS layout** (sugerido):

	```
    /opt/keystone/
      bin/keystone-agent
      runtime/
        components/<name>/<version>/
        artifacts/<hash>/
        work/<component>/
      releases/keystone-<semver>/
      current -> releases/keystone-<semver>  # self-update atómico
      logs/
      etc/ (config)
    ```

---

## Especificación de “Recipe” (descriptor de componente)

Formato YAML minimalista, inspirado en Greengrass pero propio:

```yaml
apiVersion: keystone.wisewolf/v1
kind: Component
metadata:
  name: com.keylab.hello
  version: 1.0.0
spec:
  description: "Hello edge"
  type: generic           # generic | lambda | container
  configSchema:           # opcional (JSON Schema)
    type: object
    properties:
      interval: { type: integer, minimum: 1, default: 30 }
      message:  { type: string, default: "Hello from Keystone" }

  defaults:               # valores por defecto sobreescribibles en deployment
    interval: 30
    message:  "Hello from Keystone"

  artifacts:
    - uri: https://artifacts.example.com/hello-1.0.0-linux-amd64.tar.gz
      sha256: "<HASH>"
      unpack: true

  lifecycle:
    install:
      requirePrivilege: false
      script: |
        chmod +x ./hello
    run:
      exec:
        command: "./hello"
        args: ["--interval", "{{ config.interval }}", "--message", "{{ config.message }}"]
        env:
          LOG_LEVEL: "info"
        workingDir: "."
      restartPolicy: always     # never | on-failure | always
      health:
        check: "tcp://127.0.0.1:8080"  # http://…, tcp://…, cmd:…
        interval: 10s
        timeout: 3s
        failureThreshold: 3
    shutdown:
      script: |
        echo "bye"

  security:
    runAs: "ks_user:ks_group"
    capabilities:            # POSIX caps si es root; ignorado si no
      - CAP_NET_BIND_SERVICE
    seccompProfile: "default"  # default | unconfined | path:/etc/keystone/seccomp.json
    apparmorProfile: "ks-default"

  resources:
    cpuQuota: 40         # %
    memoryLimit: 128Mi
    openFiles: 4096

  dependencies:
    - name: com.keylab.sidecar
      version: ">=1.0.0 <2.0.0"
      type: hard         # hard | soft

  permissions:
    files:
      - path: /var/lib/keylab/
        mode: "0750"
        owner: "ks_user:ks_group"
    devices:
      - /dev/ttyUSB0
```

**Notas**:

- Plantillas estilo `{{ config.* }}` para interpolar la configuración efectiva.
- `health` soporta chequeo HTTP, TCP o comando shell.
- `restartPolicy` la aplica el **supervisor interno** (no systemd).
- `security` y `resources` se traducen a cgroups/rlimits/seccomp cuando existan.

---

## Ciclo de vida y máquina de estados

Estados por componente: `NONE → INSTALLED → STARTING → RUNNING → DEGRADED → STOPPING → STOPPED → FAILED`.

- **Supervisor loop** (por componente):
	1. Resolver **DAG de dependencias**.
	2. Descargar/verificar artefactos.
	3. Ejecutar `install` si cambia versión.
	4. `run.exec` (o `container.run`) bajo `runAs`.
	5. **Health monitor** con backoff exponencial.
	6. Aplicar `restartPolicy` y límites de reintentos.
	7. Emitir eventos (`component.state`, `component.health`).

Rollback:

- Si falla `install` o `run` en nueva versión, revertir a la previa (si existe).
- Mantener **N** versiones en caché (GC por LRU y capacidad).

## Despliegues OTA

**Plan de despliegue**:

- Entrada: *manifest* de despliegue (lista de componentes+versiones+overrides).
- Resolver **orden topológico** (dependencias).
- Aplicar en pasos idempotentes; cada paso registra checkpoint.
- **Atómico por componente** (no por flota): si una pieza falla, no avanza a dependientes.
- **Canary** por “anillos” de dispositivos (groups): `canary → beta → prod`.

**Self-update del Agent**:

- Descargar `keystone-agent` firmado + metadata (TUF-like).
- Instalar en `releases/keystone-x.y.z` y **atomic symlink switch** `current → x.y.z`.
- `systemd` reinicia el servicio; si el nuevo agente no reporta healthy en T segundos, revertir symlink y reiniciar anterior.

## Seguridad

- **Identidad** del dispositivo: par de claves + cert X.509 (CSR en el edge o provisión por fábrica).
- **mTLS** en todas las conexiones de control.
- **Firma de artefactos**: manifest de despliegue y artefactos con firma (ed25519 o x509). Verificación previa a instalar.
- **Aislamiento**:
	- Usuario/grupo dedicados por componente,
	- cgroups v2 (CPU/mem/pids/io),
	- seccomp profiles por clase de componente,
	- AppArmor/SELinux si disponible.
- **Secret store** local cifrado (age/sops), con *enveloping* vía KMS/HSM/TPM opcional.

## Observabilidad

- **Logs**: JSON estructurado; *sinks* configurables (archivo local, syslog, MQTT/HTTP, Loki).
- **Métricas**: `/metrics` con Prometheus; KPIs clave (latencia de despliegue, éxito/fallo por paso, RAM/CPU por componente, GC de artefactos).
- **Eventos**: bus interno + exportadores (MQTT topics `keystone/+/events`).
- **Trazas** (opcional): OpenTelemetry al backend que quieras.

## API y CLI

- **API local** (Unix socket + HTTP):
	- `GET /v1/components`, `POST /v1/deployments`, `POST /v1/components/{id}:restart`, etc.
- **CLI** (`keystonectl`):
	- `keystonectl component list|logs|restart`
	- `keystonectl deploy apply -f deployment.yaml`
	- `keystonectl doctor` (diagnóstico, permisos, conectividad).
- **SDK** (Go y TS opcional) para crear herramientas de integración.

## Skeleton de código (Go)

```go
// runner.go
type Runner interface {
    Prepare(ctx context.Context, comp ComponentSpec) error
    Start(ctx context.Context, comp ComponentSpec) (ProcessHandle, error)
    Stop(ctx context.Context, handle ProcessHandle) error
    Health(ctx context.Context, handle ProcessHandle) HealthStatus
}

type ProcessRunner struct{ /* deps: cgroups, seccomp, logger */ }
type ContainerRunner struct{ /* deps: CRI client, logger */ }

// supervisor.go
type Supervisor struct {
    runnerRegistry map[string]Runner // "generic"->ProcessRunner, "container"->ContainerRunner
    store          StateStore
    events         EventBus
}

func (s *Supervisor) Reconcile(ctx context.Context, desired DeploymentPlan) error {
    dag := BuildDAG(desired.Components)
    for _, comp := range dag.Topological() {
        if err := s.applyComponent(ctx, comp); err != nil {
            s.events.Emit(Failed(comp, err))
            s.rollback(ctx, comp)
            return err
        }
    }
    return nil
}

func (s *Supervisor) applyComponent(ctx context.Context, comp ComponentSpec) error {
    // 1) artifacts
    if err := s.fetchAndVerify(ctx, comp.Artifacts); err != nil { return err }
    // 2) install
    if err := s.runHook(ctx, comp.Lifecycle.Install); err != nil { return err }
    // 3) start
    h, err := s.runnersFor(comp).Start(ctx, comp); if err != nil { return err }
    // 4) health
    return s.watchHealth(ctx, comp, h)
}
```

---

## Ejemplos rápidos de receta

**TOML es una muy buena opción** para las recetas de KeyStone: es legible, predecible y con menos “sorpresas” que YAML. Para un orquestador de edge ligero, prioriza la edición humana y la robustez del *parser*, y en eso TOML brilla. Aun así, conviene asumir algunas limitaciones y fijar convenciones desde el principio.

**Elegimos TOML** para las **recetas** en el MVP por:
- Simplicidad de edición en campo.
- Menos ambigüedades y “magia”.
- Buen soporte en Go y *parsing* rápido.

Y añade tres “barandillas” en el agente:
1. **Imports opcionales** (si algún día quieres reutilizar trozos):
`imports = ["./common.recipe.toml"]`

Implementa el *merge* tú, de forma determinista.

2. **Validación** (mínima al principio, ampliable):
- Carga TOML → conviértelo a `map[string]any` → valida con **JSON Schema** o CUE cuando te apetezca endurecer.

2. **Convenciones** claras:

- Duraciones siempre como strings (`"10s"`).
- “No definido” = ausencia de clave (evitar `null` semántico).
- Sin anclas mágicas (no existen en TOML, perfecto).

> Si más adelante necesitas plantillado complejo, puedes:
> a) mantener TOML y añadir un *preprocesador* simple (imports + sustitución `${var}`), o
> b) conservar TOML “canónico” y validar/unificar con **CUE** *por dentro* (sin exponer CUE al usuario final).

### Ventajas de TOML para recetas

- **Simplicidad y legibilidad**: sintaxis mínima, ideal para que técnicos de campo editen a mano sin romper tipos.
- **Tipado claro**: enteros, booleanos, fechas/horas; evita ambigüedades típicas de YAML (por ejemplo, `on`, `yes`…).
- **Strings multilínea**: comillas triples útiles para *scripts* de lifecycle.
- **Estructuras naturales**: *arrays-of-tables* para listas de artefactos, *dependencies*, *ports*, etc.
- **Soporte en Go**: `pelletier/go-toml/v2` y `BurntSushi/toml` son maduros y rápidos.

### Precauciones y cómo mitigarlas

1. **Sin `null` nativo**
	Use ausencia de clave para “no definido”. Para “vaciar” un valor, establezca un *sentinel* (p. ej., `""` o `[]`) y documente la semántica.
2. **Sin anclas/merge de YAML**
	Si necesita reutilización, añada **imports** en KeyStone (p. ej., `imports = ["common.toml"]`) y haga el *merge* usted en el agente.
3. **Duraciones**
	TOML no tiene tipo duración. Estandarice strings `10s`, `500ms`, `1h30m` y parse con `time.ParseDuration`.
4. **Binarios/plantillas grandes**
	No embeba binarios; refiéralos como artefactos. Si necesita *inline*, use `base64` con prefijo (`"base64:…"`).
5. **Validación de esquema**
	No existe “TOML Schema” estándar. Convierta TOML→JSON y valide con **JSON Schema** o, mejor aún, con **CUE** (que además permite *defaults* y constraints).
6. **Preservación de comentarios**
	La mayoría de *parsers* no los conservan al reescribir. Evite reserializar recetas del usuario; trate los TOML como **entrada de solo lectura** y guarde el estado aparte.

### Convenciones recomendadas

- **Separar** “receta” del **manifest de despliegue** (overrides).
	Precedencia: `defaults < device < deployment`.
- **Interpolación** solo en campos permitidos, con un marcador claro (p. ej., `${config.interval}`).
- **Nombres estables** en minúsculas con puntos: `com.company.componente`.
- **Archivos**: `*.recipe.toml` y `deployment.toml`.

### Ejemplo de receta en TOML (proceso nativo)

```toml
# com.wisewolf.hello.recipe.toml
[metadata]
name = "com.wisewolf.hello"
version = "1.0.0"
description = "Demo component that prints heartbeats"
publisher = "Wisewolf Labs"
type = "generic"         # generic | container | lambda (futuro)

[config.schema]          # opcional: clave para referencia a JSON Schema/CUE
ref = "file://schemas/com.wisewolf.hello.json"

[config.defaults]
log_level = "INFO"
interval_seconds = 30
message = "Hello from Keystone"

[[artifacts]]
uri = "https://artifacts.example.com/hello/1.0.0/hello-linux-amd64"
sha256 = "…"
unpack = false

[lifecycle.install]
require_privilege = false
script = """
chmod +x ./hello
"""

[lifecycle.run.exec]
command = "./hello"
args = ["--log-level", "${config.log_level}", "--interval", "${config.interval_seconds}", "--message", "${config.message}"]
env.LOG_LEVEL = "${config.log_level}"
working_dir = "."

[lifecycle.run.health]
check = "tcp://127.0.0.1:8080"
interval = "10s"
timeout = "3s"
failure_threshold = 3
restart_policy = "always"  # never | on-failure | always

[lifecycle.shutdown]
script = "echo 'bye'"

[security]
run_as = "ks_user:ks_group"
seccomp_profile = "default"   # default | unconfined | path:/etc/keystone/seccomp.json
apparmor_profile = "ks-default"
capabilities = ["CAP_NET_BIND_SERVICE"]

[resources]
cpu_quota = 40        # porcentaje
memory_limit = "128Mi"
open_files = 4096

[[dependencies]]
name = "com.wisewolf.sidecar"
version = ">=1.0.0 <2.0.0"
type = "hard"
```

### Ejemplo (contenedor con containerd/nerdctl)

```toml
[metadata]
name = "com.wisewolf.web"
version = "1.0.0"
type = "container"

[container]
image = "registry.example.com/wisewolf/web:1.0.0"
pull_policy = "IfNotPresent"
ports = ["8080:8080"]
env.LOG_LEVEL = "info"
restart_policy = "always"

[security]
seccomp_profile = "default"

[resources]
cpu_quota = 50
memory_limit = "256Mi"
```

### Carga y validación en Go (boceto)

```go
import (
  "encoding/json"
  "github.com/pelletier/go-toml/v2"
  "os"
)

type Recipe struct {
  Metadata struct {
    Name, Version, Description, Publisher, Type string
  } `toml:"metadata"`
  // … defina structs para lifecycle, artifacts, security, etc.
}

func LoadRecipe(path string) (*Recipe, map[string]any, error) {
  b, err := os.ReadFile(path)
  if err != nil { return nil, nil, err }

  var r Recipe
  if err := toml.Unmarshal(b, &r); err != nil { return nil, nil, err }

  // Para validación con JSON Schema: TOML -> map -> JSON
  var generic map[string]any
  if err := toml.Unmarshal(b, &generic); err != nil { return nil, nil, err }
  // jsonBytes, _ := json.Marshal(generic); // valide con su motor favorito

  return &r, generic, nil
}
```

### Conclusión

Adelante con **TOML** para recetas: encaja perfectamente con la filosofía “ligero y humano” de KeyStone. Fije desde ya un **esquema canónico**, reglas de interpolación y una **capa de imports**; con eso tendrá un sistema robusto, sencillo y agradable de operar. Si lo desea, preparo el **JSON Schema/CUE de referencia** y las estructuras Go completas para *unmarshal* + validación.

## Plan de entrega (MVP → v1.0)

**Fase 0 (semana 1–2) — Bootstrap**

- Esqueleto del agente, config base, logging, systemd unit.
- State store, layout de disco, API `/healthz`.

**Fase 1 (semana 3–6) — Supervisor + ProcessRunner**

- Recipe parser, lifecycle hooks, healthchecks.
- Artifact manager (HTTP/S3), verificación SHA256.
- cgroups/rlimits básicos, `runAs`, restart policy.

**Fase 2 (semana 7–9) — Deployments + Rollback**

- DAG de dependencias, checkpoints, rollback automático.
- CLI `keystonectl`, métricas Prometheus.

**Fase 3 (semana 10–12) — Seguridad & Offline**

- mTLS (bootstrap de identidad), firma de artefactos (ed25519).
- Cola local de jobs, backoff, re-envío.

**Fase 4 (semana 13–15) — ContainerRunner (opcional)**

- Integración containerd/nerdctl (rootless si procede).
- Mapeo de puertos/dispositivos y políticas de permisos.

**Fase 5 (semana 16–18) — Self-update & Canary**

- Releases atómicas, revert automático.
- Anillos de despliegue, telemetría de éxito/fallo.

## Buenas prácticas operativas

- **Usuarios dedicados** por componente; evitar root salvo `cap_net_bind_service` puntuales.
- **Perfiles seccomp** por clase de componente (web, sensor, pipeline).
- **GC de artefactos** por cuota (p. ej., 2 GB) y LRU.
- **Health end-to-end**: *liveness* del proceso y *readiness* de la función.
- **Pruebas de caos**: matar procesos, cortar red, corromper descargas → debe recuperar.
- **Contrato de compatibilidad** del Recipe (semver + migraciones de config).

## Riesgos y mitigaciones

- **Fragmentación de runners** → API estable `Runner` y harness de tests con *contracts*.
- **Seguridad mal configurada** → plantillas de perfiles seguros por defecto + “doctor”.
- **Soporte de CRI** en distros viejas → mantener ProcessRunner como *happy path*.
- **Consumo de disco** en caché → GC agresivo + límites por componente.

## Licencia y compliance

- **Apache-2.0** para maximizar adopción.
- SBOM generado (syft) para el binario del agente.
	- Firmas *detached* (cosign/age) opcionales en pipelines.

## Siguiente paso recomendado

Definir **casos de uso iniciales** (2–3 componentes reales), fijar **KPIs de consumo/fiabilidad**, y arrancar con el **MVP (Fase 0–2)**. Puedo prepararte el **repo inicial** (estructura de módulos, `Makefile`, `systemd unit`, y un componente de ejemplo) y un **ADR-0001** que formalice “procesos nativos por defecto; contenedores como opción”.

## Librerías

A continuación tienes un “kit de piezas” en Go para construir el ciclo de vida de procesos en KeyStone (arranque, parada, monitorización y actualización), con librerías muy utilizadas en producción y un par de patrones de implementación. La idea es que puedas empezar solo con la stdlib y añadir capacidades (cgroups, seccomp, firma, etc.) según lo necesites.

### 1) Núcleo de procesos (arranque/parada/restarts)

**Imprescindibles (stdlib):**

- `os/exec`, `context`, `os/signal`, `syscall`, `time`, `io`
	Para lanzar procesos, cancelarlos con *timeouts*, capturar `stdout/stderr`, reenviar señales (`SIGTERM`/`SIGKILL`) y limpiar *zombies*.

**Utilidades para orquestación:**

- `golang.org/x/sync/errgroup` – coordinación de goroutines y errores.
- `github.com/cenkalti/backoff/v4` – *retries* con *exponential backoff* (reinicios y reintentos de health checks).
- `github.com/looplab/fsm` – máquina de estados por componente (RUNNING/FAILED/…).
- `github.com/heimdalr/dag` – resolución de dependencias (DAG) en despliegues.

**Patrón mínimo de supervisión (esqueleto):**

```go
cmd := exec.CommandContext(ctx, bin, args...)
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // grupo propio para señales
stdout, _ := cmd.StdoutPipe()
stderr, _ := cmd.StderrPipe()

if err := cmd.Start(); err != nil { /* handle */ }

// lectura no bloqueante de logs
go stream(stdout, compLogger.Info)
go stream(stderr, compLogger.Error)

// parada ordenada con timeout
stop := func() error {
    // SIGTERM al grupo de proceso
    pgid := -cmd.Process.Pid
    syscall.Kill(pgid, syscall.SIGTERM)
    done := make(chan error, 1)
    go func() { done <- cmd.Wait() }()
    select {
    case err := <-done:
        return err
    case <-time.After(10 * time.Second):
        syscall.Kill(pgid, syscall.SIGKILL)
        return <-done
    }
}
```

### 2) Aislamiento y límites (opcional pero recomendable)

**cgroups v2 (CPU/Mem/PIDs/IO):**

- `github.com/containerd/cgroups/v3` – crear y asignar el PID del proceso al cgroup; funciona en v1/v2 (elige v3 si usas v2).

**Capacidades POSIX:**

- `github.com/syndtr/gocapability/capability` – añadir/quitar *capabilities* (p. ej., `CAP_NET_BIND_SERVICE`) cuando ejecutes como root reducido.

**Seccomp (filtro de syscalls):**

- `github.com/seccomp/libseccomp-golang` – carga de perfiles seccomp. Úsalo en un *pre-exec hook* para aplicar el perfil justo antes del `exec()`.

**Límites POSIX:**

- `golang.org/x/sys/unix` – `Setrlimit`, *nice/ionice*, *namespaces* cuando aplique.

> Sugerencia operativa: por defecto corre como usuario/grupo dedicados (`ks_user:ks_group`); eleva solo lo imprescindible con *capabilities*. Activa seccomp “default-deny” para clases de componentes con superficie pequeña (workers, parsers, etc.).

### 3) Monitorización (health & recursos)

**Métricas del proceso:**

- `github.com/shirou/gopsutil/v4/process` – CPU/mem/FDs por PID para exponer métricas por componente (útil para *SLOs*).

**Health checks:**

- stdlib (`net/http`, `net`) para probes HTTP/TCP.
- Ejecución de *probes* por comando (shell) usando `exec.CommandContext`.

**Observabilidad:**

- Logs: `go.uber.org/zap` (o `github.com/rs/zerolog`) – estructurado y de alto rendimiento.
- Métricas: `github.com/prometheus/client_golang` – expón `/metrics`.
- Trazas (opcional): `go.opentelemetry.io/otel`.

### 4) Descarga de artefactos y verificación

**Descarga con reintentos y *resume*:**

- `github.com/hashicorp/go-retryablehttp` – HTTP robusto.
- `github.com/cavaliergopher/grab/v3` – descargas con *resume*, *checksums* y *progress* (muy práctico para OTA).

**Descompresión:**

- stdlib (`archive/tar`, `compress/gzip`, `archive/zip`) + `github.com/klauspost/compress` para rendimiento si tratas ficheros grandes.

**Checksums y firma:**

- Checksums: stdlib `crypto/sha256`.
- Firma ligera: `golang.org/x/crypto/ed25519` (tu propio manifiesto firmado) o **TUF**:
	- `github.com/theupdateframework/go-tuf` – *framework* probado para *update metadata* (roles, *thresholds*, *rollback protection*).
- Si ya usas ecosistema OCI: **cosign** (sigstore) tiene SDK, pero es más pesado; puedes integrarlo más adelante.

### 5) Actualización del propio agente (self-update seguro)

- **Estrategia A/B con symlink atómico** (sencilla y robusta): descarga en `/opt/keystone/releases/x.y.z/`, verifica firma, cambia `current -> x.y.z`, reinicia servicio, *watchdog* y rollback si no reporta *healthy*.
- Librería útil (si usas GitHub para *releases*): `github.com/rhysd/go-github-selfupdate`.
	En entornos cerrados, implementa tú el *switch atómico* (es trivial) y añade verificación con TUF/ed25519.

### 6) Configuración, secretos y almacenamiento local

- Config merge y *hot-reload*: `github.com/knadh/koanf` (más simple y determinista que Viper; soporta YAML/JSON/env/flags).
- Secret store local: usa archivos cifrados con `filippo.io/age` o integra con KMS/TPM si procede.
- Estado local:
	- **SQLite**: `github.com/mattn/go-sqlite3` (rendimiento) o `modernc.org/sqlite` (pure Go).
	- KV embebido: `go.etcd.io/bbolt` (simple) o `github.com/dgraph-io/badger/v4` (alto rendimiento).

### 7) Integraciones con *container runtimes* (si ofreces runner de contenedores)

- **containerd**: `github.com/containerd/containerd` + `github.com/containerd/nerdctl/pkg/inspecttypes` (si usas nerdctl como *frontend*).
- **Docker** (si ya está desplegado): `github.com/docker/docker/client`.
- Plantéate empezar **solo con ProcessRunner** y añadir ContainerRunner después; simplifica el MVP y evita dependencias del kernel.

3## 8) Sistema y *boot*

- Systemd *readiness*: `github.com/coreos/go-systemd/v22/daemon` – notifica `READY=1` cuando el agente esté operativo (útil para integrarte bien con *service watchdog*).
- *File locking* para exclusión entre procesos/instancias: `github.com/gofrs/flock`.
- *Filesystem events* (p. ej., *hot reload* de recetas): `github.com/fsnotify/fsnotify`.

### 9) CLI y API

- CLI: `github.com/alecthomas/kong` o `github.com/spf13/cobra` (Kong es más conciso y robusto con tipos).
- API local HTTP: stdlib + `github.com/go-chi/chi/v5` (router ligero) o `github.com/gin-gonic/gin` (más *batteries-included*).

## Mapa de “qué usar para cada cosa”

- **Arranque/Parada/Señales:** stdlib (`exec`, `os/signal`, `syscall`), con `Setpgid` y *timeouts*.
- **Reintentos y *backoff*:** `cenkalti/backoff`.
- **FSM + Dependencias:** `looplab/fsm` + `heimdalr/dag`.
- **Health & Recursos:** `gopsutil/process` + probes HTTP/TCP.
- **Límites/Aislamiento:** `containerd/cgroups` + `gocapability` + `libseccomp-golang` + `unix.Setrlimit`.
- **Descargas OTA:** `retryablehttp` + `grab` + `sha256`.
- **Firma/Anti-rollback:** `go-tuf` (o `ed25519` simple al principio).
- **Self-update:** symlink atómico + `go-github-selfupdate` (si usas GH).
- **Métricas/Logs:** Prometheus client + Zap/Zerolog.
- **Config/Estado:** Koanf + SQLite/BoltDB.
- **Systemd readiness:** `go-systemd/daemon`.

## Detalle clave: *pre-exec hooks* para seguridad

Cuando quieras aplicar **seccomp/capabilities/rlimits** al proceso hijo, utiliza `Cmd.SysProcAttr` y un *hook* previo al `exec()`:

```go
cmd := exec.CommandContext(ctx, bin, args...)
cmd.SysProcAttr = &syscall.SysProcAttr{
    Credential: &syscall.Credential{Uid: uid, Gid: gid},
    // Namespaces opcionales...
}
cmd.ExtraFiles = nil
// Antes de Start(): configura cgroup y añade el PID tras Start().
// Para seccomp: usa clone+prctl en el propio hijo vía libseccomp (requiere cgo).
```

Con seccomp en Go “puro”, la práctica es invocar una *stub helper* (pequeño binario) que aplique el perfil y luego *exec* la carga, o emplear cgo con libseccomp en el mismo proceso.

---

## Recomendación para tu MVP (3–4 semanas)

1. **ProcessRunner + Supervisor** con stdlib, `backoff`, `errgroup`, `fsm`.
2. **Health** (HTTP/TCP/cmd) + métricas Prometheus.
3. **OTA** con `retryablehttp` + `grab` + `sha256` y **A/B self-update** por symlink.
4. **Límites básicos**: `Setrlimit` y cgroups CPU/mem (containerd/cgroups).
5. **Firma simple** con `ed25519` (migra a TUF más tarde).
6. **Koanf** para *overrides* de config y **SQLite** para estado.

Cuando eso esté estable, añade **seccomp/capabilities** y, si tu parque lo pide, el **ContainerRunner** con `containerd`.

El `go.mod` de arranque tendría las dependencias anteriores y el proyecto una estructura de paquetes (`runner/`, `supervisor/`, `agent/`, `store/`, `ota/`, `security/`, `api/`, `cli/`).

---

## Ejemplo autocontenido en Go de KeyStone

Perfecto, Charlie. Aquí tienes un **ejemplo autocontenido** en Go que combina:

- **FSM** (con `github.com/looplab/fsm`) para el ciclo de vida de cada servicio.
- **DAG + topological sort** (implementado con Kahn) para respetar dependencias y **arrancar en paralelo** los servicios de cada “capa”.

El ejemplo arranca tres servicios ficticios (`db`, `cache`, `api`) donde:

- `db` no tiene dependencias,
- `cache` depende de `db`,
- `api` depende de `db` y `cache`.

Los comentarios en el código están **en inglés** (como prefieres).

---

### `go.mod`

```go
module example.com/keystone-fsm-dag-demo

go 1.21

require github.com/looplab/fsm v0.2.0
```

---

### `main.go`

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/looplab/fsm"
)

// ----------------------------
// Domain model
// ----------------------------

// Component models a service with a lifecycle FSM and dependencies.
// For an MVP, StartFn/StopFn can wrap os/exec to run real processes.
type Component struct {
	Name string
	Deps []string

	mu     sync.Mutex
	FSM    *fsm.FSM
	StartFn func(ctx context.Context) error
	StopFn  func(ctx context.Context) error
}

// NewComponent builds a component with a default FSM:
// none -> installing -> starting -> running
// Any error triggers 'fail' and transitions to 'failed'.
func NewComponent(name string, deps []string, startFn, stopFn func(ctx context.Context) error) *Component {
	c := &Component{
		Name:   name,
		Deps:   deps,
		StartFn: startFn,
		StopFn:  stopFn,
	}
	c.FSM = fsm.NewFSM(
		"none",
		fsm.Events{
			{Name: "install", Src: []string{"none"}, Dst: "installing"},
			{Name: "start",   Src: []string{"installing", "stopped"}, Dst: "starting"},
			{Name: "up",      Src: []string{"starting"}, Dst: "running"},
			{Name: "stop",    Src: []string{"running", "starting"}, Dst: "stopped"},
			{Name: "fail",    Src: []string{"installing", "starting", "running"}, Dst: "failed"},
		},
		fsm.Callbacks{
			"enter_state": func(e *fsm.Event) {
				log.Printf("[%-8s] %s -> %s", c.Name, e.Src, e.Dst)
			},
		},
	)
	return c
}

// Install emulates installation work (idempotent).
func (c *Component) Install(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.FSM.Current() == "installing" || c.FSM.Current() == "starting" || c.FSM.Current() == "running" {
		return nil // already moving forward
	}
	if err := c.FSM.Event("install"); err != nil {
		return err
	}
	// Simulate some install work
	select {
	case <-time.After(200 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Start executes the user StartFn and drives the FSM to 'running' or 'failed'.
func (c *Component) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.FSM.Current() == "running" {
		return nil
	}
	if c.FSM.Current() == "none" {
		if err := c.FSM.Event("install"); err != nil {
			return err
		}
	}
	if c.FSM.Current() == "installing" || c.FSM.Current() == "stopped" {
		if err := c.FSM.Event("start"); err != nil {
			return err
		}
	}
	// Release lock while running user function to avoid blocking other ops
	c.mu.Unlock()
	err := c.StartFn(ctx)
	c.mu.Lock()

	if err != nil {
		_ = c.FSM.Event("fail")
		return err
	}
	if err := c.FSM.Event("up"); err != nil {
		return err
	}
	return nil
}

// Stop invokes StopFn and moves to 'stopped'.
func (c *Component) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.FSM.Current() {
	case "running", "starting":
		if c.StopFn != nil {
			c.mu.Unlock()
			err := c.StopFn(ctx)
			c.mu.Lock()
			if err != nil {
				_ = c.FSM.Event("fail")
				return err
			}
		}
		return c.FSM.Event("stop")
	default:
		return nil
	}
}

// ----------------------------
// DAG + Topological layers (Kahn's algorithm)
// ----------------------------

type Graph struct {
	Nodes map[string]*Component
	Edges map[string][]string // from -> to (dependency -> dependent)
	InDeg map[string]int
}

func BuildGraph(comps []*Component) *Graph {
	g := &Graph{
		Nodes: map[string]*Component{},
		Edges: map[string][]string{},
		InDeg: map[string]int{},
	}
	for _, c := range comps {
		g.Nodes[c.Name] = c
		if _, ok := g.InDeg[c.Name]; !ok {
			g.InDeg[c.Name] = 0
		}
	}

	// Edge: dep -> comp (you can start 'comp' only after 'dep' is running)
	for _, c := range comps {
		for _, dep := range c.Deps {
			g.Edges[dep] = append(g.Edges[dep], c.Name)
			g.InDeg[c.Name]++
		}
	}
	return g
}

// TopoLayers returns layers of nodes; each layer can be started concurrently.
func (g *Graph) TopoLayers() ([][]string, error) {
	inDeg := make(map[string]int, len(g.InDeg))
	for k, v := range g.InDeg {
		inDeg[k] = v
	}

	// Collect initial zero in-degree nodes
	var q []string
	for n, d := range inDeg {
		if d == 0 {
			q = append(q, n)
		}
	}

	var layers [][]string
	visited := 0

	for len(q) > 0 {
		// current layer
		layer := append([]string{}, q...)
		layers = append(layers, layer)
		q = q[:0]

		for _, u := range layer {
			visited++
			for _, v := range g.Edges[u] {
				inDeg[v]--
				if inDeg[v] == 0 {
					q = append(q, v)
				}
			}
		}
	}

	if visited != len(g.Nodes) {
		return nil, errors.New("cycle detected or missing nodes in DAG")
	}
	return layers, nil
}

// ----------------------------
// Orchestrator
// ----------------------------

// StartOrchestrated starts all components respecting the DAG, running each layer in parallel.
// If any component in a layer fails, it cancels the context and stops previously started components.
func StartOrchestrated(parent context.Context, comps []*Component) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	graph := BuildGraph(comps)
	layers, err := graph.TopoLayers()
	if err != nil {
		return err
	}
	log.Printf("Layers: %+v", layers)

	started := make(map[string]*Component)

	for i, layer := range layers {
		log.Printf("== Starting layer %d: %v ==", i, layer)

		// Start all services in this layer concurrently
		var wg sync.WaitGroup
		errCh := make(chan error, len(layer))

		for _, name := range layer {
			c := graph.Nodes[name]
			wg.Add(1)
			go func(comp *Component) {
				defer wg.Done()

				// Install + Start with a timeout per component
				compCtx, cancelComp := context.WithTimeout(ctx, 5*time.Second)
				defer cancelComp()

				if err := comp.Install(compCtx); err != nil {
					errCh <- fmt.Errorf("%s install: %w", comp.Name, err)
					return
				}
				if err := comp.Start(compCtx); err != nil {
					errCh <- fmt.Errorf("%s start: %w", comp.Name, err)
					return
				}

				// Consider the component "ready" at this point.
				// In a real system, you'd wait for a health probe to pass.
				log.Printf("[%-8s] is RUNNING", comp.Name)
				started[comp.Name] = comp
			}(c)
		}

		wg.Wait()
		close(errCh)

		// If any failed, rollback/stop what was started and abort
		if startErr := <-errCh; startErr != nil {
			log.Printf("Layer %d failed: %v", i, startErr)
			cancel() // signal all goroutines to stop any ongoing work

			// Best-effort stop of already started components (reverse order)
			for j := i; j >= 0; j-- {
				for _, name := range layers[j] {
					if comp, ok := started[name]; ok {
						_ = comp.Stop(context.Background())
					}
				}
			}
			return startErr
		}
	}

	log.Printf("All components are RUNNING")
	return nil
}

// ----------------------------
// Demo Start/Stop functions
// ----------------------------

func mockStart(label string, boot time.Duration) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		// Simulate some boot sequence with cancellable wait
		select {
		case <-time.After(boot):
			log.Printf("[%-8s] boot sequence completed", label)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func mockStop(label string, shutdown time.Duration) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-time.After(shutdown):
			log.Printf("[%-8s] stopped", label)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ----------------------------
// main
// ----------------------------

func main() {
	// Define three components with dependencies:
	// db -> (cache) -> (api depends on db, cache)
	db := NewComponent("db", nil, mockStart("db", 400*time.Millisecond), mockStop("db", 150*time.Millisecond))
	cache := NewComponent("cache", []string{"db"}, mockStart("cache", 600*time.Millisecond), mockStop("cache", 150*time.Millisecond))
	api := NewComponent("api", []string{"db", "cache"}, mockStart("api", 900*time.Millisecond), mockStop("api", 150*time.Millisecond))

	comps := []*Component{db, cache, api}

	ctx := context.Background()
	if err := StartOrchestrated(ctx, comps); err != nil {
		log.Fatalf("orchestration error: %v", err)
	}

	// Keep them "running" for a short while and then stop gracefully
	time.Sleep(1200 * time.Millisecond)
	log.Println("Shutting down stack...")

	// Stop in reverse topological order for cleanliness (here, manually)
	_ = api.Stop(context.Background())
	_ = cache.Stop(context.Background())
	_ = db.Stop(context.Background())

	log.Println("Done.")
}
```

---

### Qué enseña este ejemplo

1. **FSM por servicio**: estados `none → installing → starting → running`, con transiciones claras y callback de logging.
2. **DAG con *layers***: cálculo de capas topológicas; cada capa arranca **en paralelo**.
3. **Orquestación con cancelación y rollback**: si falla algo en una capa, se cancela el contexto, se paran los servicios ya iniciados y se aborta.
	
4. **Listo para evolucionar**: donde hoy usamos `mockStart/Stop`, en KeyStone conectarías tus `StartFn/StopFn` a `os/exec` (procesos nativos), cgroups/seccomp, health checks, etc.

---

### Cómo ejecutarlo

```bash
mkdir -p keystone-fsm-dag-demo && cd keystone-fsm-dag-demo
# Copia go.mod y main.go
go mod tidy
go run .
```

---

### Variantes y siguientes pasos

- **Añadir health checks reales**: espera activa a una sonda HTTP/TCP antes del evento `up`.
- **Retries/backoff**: integra `github.com/cenkalti/backoff/v4` cuando un start falle de forma transitoria.
- **Paralelismo controlado**: limita el número de servicios simultáneos por capa (p. ej., con un semáforo `chan struct{}`).
- **Stop ordenado por DAG**: igual que el arranque, pero recorriendo capas en orden inverso.

Si quieres, te preparo la **versión con `os/exec`**, manejo de señales y aislamiento básico (cgroups/rlimits) para integrar directamente con procesos nativos en tu MVP de KeyStone.
