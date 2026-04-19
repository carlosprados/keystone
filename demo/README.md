# Demo de Keystone — despliegue y actualización de 3 componentes

Esta demo muestra cómo Keystone resuelve dependencias, descarga artefactos por
HTTP con verificación SHA-256, arranca procesos nativos en el orden correcto y
hace una **actualización atómica** de los tres componentes de la versión **1.0.0**
a la **2.0.0** en una sola llamada.

## Escenario

Tres servicios Go triviales que simulan un pipeline típico edge:

| Componente      | Puerto | Depende de           | Rol                                            |
|-----------------|--------|----------------------|------------------------------------------------|
| `config`        | 7001   | —                    | Publica configuración (`rate_ms`, `greeting`…) |
| `producer`      | 7002   | `config`             | Lee la config y genera eventos                 |
| `consumer`      | 7003   | `config`, `producer` | Consume eventos del producer, mantiene stats   |

Grafo de dependencias:

```
config  ─┐
         ├─►  producer  ──►  consumer
         └─────────────────►
```

Al aplicar el plan, Keystone:

1. Resuelve el DAG (topological sort).
2. Arranca `config` primero, espera a que su health-check pase.
3. Arranca `producer` (ya puede leer `http://localhost:7001/config`).
4. Arranca `consumer` cuando producer está sano.

Las diferencias entre v1 y v2 son visibles a simple vista:

|                     | v1                          | v2                                              |
|---------------------|-----------------------------|-------------------------------------------------|
| `config.rate_ms`    | 1000                        | 400 (más rápido)                                |
| `config.greeting`   | "Hola equipo — config v1"   | "Hola equipo — config v2 (más rápido…)"         |
| `config.enriched`   | `false`                     | `true`                                          |
| Evento del producer | `{id, greeting, ts}`        | `{id, greeting, ts, producer_version, hostname, enriched}` |
| `/stats` consumer   | básico                      | incluye `enriched_observed` y `upstream_config` |

## Estructura de carpetas

```
demo/
├── README.md               Este documento
├── services/               Código Go (módulo propio)
│   ├── go.mod
│   ├── config-service/{v1,v2}/main.go
│   ├── data-producer/{v1,v2}/main.go
│   └── data-consumer/{v1,v2}/main.go
├── recipes/
│   ├── v1/*.recipe.toml.tmpl   Plantillas con placeholder {{SHA256}}
│   ├── v1/*.recipe.toml        Generadas por build.sh
│   ├── v2/*.recipe.toml.tmpl
│   └── v2/*.recipe.toml
├── plans/
│   ├── plan-v1.toml
│   └── plan-v2.toml
├── artifacts/              Binarios compilados (servidos por keystoneserver)
└── scripts/
    ├── build.sh            Compila los 6 binarios y renderiza recipes
    ├── serve-artifacts.sh  Levanta keystoneserver :9000 sobre demo/artifacts
    ├── run-agent.sh        Levanta keystone agent :8080 con CWD = raíz del repo
    ├── apply-v1.sh         keystonectl apply demo/plans/plan-v1.toml
    ├── apply-v2.sh         keystonectl apply demo/plans/plan-v2.toml
    ├── status.sh           Snapshot del estado (agente + endpoints)
    └── clean.sh            Para plan y borra runtime/
```

## Prerrequisitos

1. Go 1.24+, Task (`taskfile.dev`) y curl.
2. Compilar los binarios del repo keystone:
   ```bash
   cd /ruta/al/repo/keystone
   task build   # genera ./keystone, ./keystonectl, ./keystoneserver
   ```
3. Los **puertos libres**: `7001, 7002, 7003, 8080, 9000`.

## Arranque (3 terminales)

Todos los scripts se ejecutan desde la **raíz del repo**.

> **Alternativa con Task.** Hay un `demo/Taskfile.yml` que envuelve todos los
> scripts. Desde la raíz del repo: `task -t demo/Taskfile.yml --list` para ver
> las tareas. Equivalencias útiles:
>
> | Script                              | Tarea equivalente                         |
> |-------------------------------------|-------------------------------------------|
> | `./demo/scripts/build.sh`           | `task -t demo/Taskfile.yml build`         |
> | `./demo/scripts/serve-artifacts.sh` | `task -t demo/Taskfile.yml serve`         |
> | `./demo/scripts/run-agent.sh`       | `task -t demo/Taskfile.yml agent`         |
> | `./demo/scripts/apply-v1.sh`        | `task -t demo/Taskfile.yml apply:v1`      |
> | `./demo/scripts/apply-v2.sh`        | `task -t demo/Taskfile.yml apply:v2`      |
> | `./demo/scripts/status.sh`          | `task -t demo/Taskfile.yml status`        |
> | `./demo/scripts/clean.sh`           | `task -t demo/Taskfile.yml clean`         |
>
> Extras del Taskfile: `probe:v1`, `probe:v2`, `graph`, `components`,
> `demo:up` (build+apply v1+probe) y `demo:upgrade` (apply v2+probe).

### Terminal 1 — repositorio de artefactos

```bash
./demo/scripts/build.sh          # compila + renderiza recipes
./demo/scripts/serve-artifacts.sh
```

Salida esperada: `keystoneserver: sirviendo …/demo/artifacts en http://:9000`.

### Terminal 2 — agente Keystone

```bash
./demo/scripts/run-agent.sh
```

Escucha en `:8080`. Deja este terminal visible: los logs en vivo son el plato
fuerte de la demo (eventos `component=… msg=starting component`, health checks,
transiciones de estado).

### Terminal 3 — operador

```bash
./demo/scripts/apply-v1.sh
./demo/scripts/status.sh
```

## Guión recomendado (aprox. 10 min)

### 1. Contexto (1 min)

> "Keystone es un orquestador edge. Nuestra filosofía es **procesos primero,
> contenedores cuando hagan falta**. Aquí os voy a enseñar cómo convergemos un
> dispositivo a un **estado deseado** descrito en TOML, con dependencias,
> health checks y actualizaciones atómicas."

### 2. Enseñar el plan y un recipe (1 min)

```bash
cat demo/plans/plan-v1.toml
cat demo/recipes/v1/com.demo.consumer.recipe.toml
```

Puntos a remarcar:

- El **plan** solo nombra componentes y sus recipes.
- Un **recipe** describe: metadata, artefactos (URI + SHA-256), instalación,
  `run.exec` (comando + env), health, dependencias.
- `consumer` declara `[[dependencies]]` sobre `com.demo.config` y
  `com.demo.producer`. Keystone construye el DAG por `metadata.name`.

### 3. Apply v1 (2 min)

```bash
./demo/scripts/apply-v1.sh
```

Mirar el log del agente: se ve cómo descarga cada binario, verifica SHA-256,
ejecuta el install script y arranca en orden `config → producer → consumer`.

```bash
./demo/scripts/status.sh
```

Comprobaciones rápidas:

```bash
curl -s localhost:7001/config  | jq
curl -s localhost:7003/stats   | jq
```

> "Cada servicio tiene su propio directorio en `runtime/components/<nombre>/<versión>/`,
> y los artefactos viven en `runtime/artifacts/<nombre>/<versión>/`. Si volvemos a
> aplicar el mismo plan, no reinstala nada: el `.installed` marker lo hace idempotente."

### 4. Mostrar el grafo (1 min)

```bash
./keystonectl graph
```

Muestra la topological order que Keystone calculó.

### 5. Update atómico v1 → v2 (3 min)

> "Ahora viene lo interesante. Voy a sustituir los tres binarios por la v2 en
> una sola llamada. El plan v2 tiene **URIs diferentes**, **SHA-256 diferentes**
> y **versión 2.0.0** en todos los recipes."

```bash
diff demo/plans/plan-v1.toml demo/plans/plan-v2.toml
diff demo/recipes/v1/com.demo.config.recipe.toml demo/recipes/v2/com.demo.config.recipe.toml

./demo/scripts/apply-v2.sh
```

En el log del agente se observa la secuencia: `config` 1.0.0 → stop, descarga
v2, instala, arranca con health OK. Luego `producer` y `consumer` hacen lo mismo.

Verificación inmediata:

```bash
curl -s localhost:7001/config  | jq     # version 2.0.0, rate_ms 400, enriched true
curl -s localhost:7003/stats   | jq     # consumer_version 2.0.0, enriched_observed > 0
curl -s localhost:7002/metrics | jq     # endpoint que solo existe en v2
```

### 6. Tolerancia (2 min, opcional)

Demostrar el supervisor parando un componente por fuera:

```bash
# Matar el PID del producer manualmente
./keystonectl components | jq '.[] | select(.name=="producer") | .pid'
kill -9 <PID>
```

El agente detecta el fallo, lo reinicia según `restart_policy = "on-failure"`
y vuelve a ponerlo healthy. El consumer, que ya tenía su config en memoria,
sobrevive sin tocarlo.

### 7. Cierre (1 min)

```bash
./demo/scripts/clean.sh
```

Para todo el plan y borra `runtime/`. Si quieres también borrar artefactos y
recipes renderizadas:

```bash
./demo/scripts/clean.sh --deep
```

## Puntos clave para remarcar

1. **Contrato declarativo**. TOML, no scripts imperativos. El agente converge.
2. **Integridad**. SHA-256 obligatorio; añadible firma ECDSA/RSA (ver
   `configs/trust/`).
3. **Dependencias explícitas**. Orden de arranque y paralelismo derivados del
   DAG — el operador no los escribe.
4. **Health-gated rollout**. Un componente nuevo no marca "running" hasta que
   su health check pasa.
5. **Idempotencia**. Reaplicar el mismo plan es barato (marker `.installed`).
6. **Procesos primero**. Sin overhead de runtime de contenedores; los binarios
   son nativos y el supervisor gestiona el PID con process groups y señales.
7. **Recovery**. El estado se persiste en `runtime/state/` — si el agente
   reinicia, reanuda los componentes que debían estar arriba.

## Troubleshooting rápido

| Síntoma                                              | Causa más probable                                       |
|------------------------------------------------------|----------------------------------------------------------|
| `bind: address already in use` al arrancar agente    | Hay un keystone previo o algo en :8080                   |
| `install script failed: … no such file`              | No se ejecutó `build.sh` o se limpió `demo/artifacts/`   |
| `sha256 mismatch` al descargar                       | Binario recompilado sin re-renderizar recipe. Re-ejecutar `build.sh` |
| `producer: config-service unreachable after 30s`     | El `config` tardó demasiado o el `CONFIG_URL` apunta mal |
| El plan viejo se reactiva al arrancar el agente      | `runtime/state/` contiene un plan previo. `clean.sh`     |

## Endpoints de referencia

### Agente Keystone (`:8080`)

- `GET /healthz` — liveness
- `GET /v1/plan/status` — estado del plan actual
- `GET /v1/components` — lista de componentes con PID, restarts, health
- `GET /v1/plan/graph` — DAG y orden topológico
- `POST /v1/plan/apply` — upload TOML (lo usa `keystonectl apply`)
- `POST /v1/plan/stop` — stop total
- `GET /metrics` — Prometheus

### Servicios de demo

- `config` — `:7001/config`, `:7001/healthz`, `:7001/version` (solo v2)
- `producer` — `:7002/events`, `:7002/healthz`, `:7002/metrics` (solo v2)
- `consumer` — `:7003/stats`, `:7003/healthz`

### Repositorio de artefactos

- `keystoneserver` — `:9000/<nombre-de-binario>`, `:9000/healthz`

## Extra: ejecutar la demo también sobre NATS o MQTT

La demo usa HTTP (`keystonectl apply`) porque es lo más simple para mostrar en
directo, pero Keystone es **multi-adapter**: puedes arrancar el agente con HTTP
+ NATS + MQTT simultáneamente y aplicar el mismo plan por cualquiera de los
tres. Útil si después del demo base queréis ver cómo se orquesta una flota
remota.

Referencia completa: `docs/adapters.md`.

### Ruta rápida con NATS (embebido en contenedor)

1. **Arranca un NATS local** (sin autenticación, solo para demo):

   ```bash
   docker run --rm -d --name nats -p 4222:4222 nats:2
   ```

2. **Relanza el agente con el adaptador NATS**, además de HTTP:

   ```bash
   ./keystone \
     --http :8080 \
     --nats-url nats://localhost:4222 \
     --nats-device-id edge-demo
   ```

3. **Aplica el plan vía NATS** con `nats` CLI (también vale `nats-top`):

   ```bash
   # Instalar: go install github.com/nats-io/natscli/nats@latest
   nats request 'keystone.edge-demo.cmd.apply' \
     "$(cat demo/plans/plan-v1.toml)" \
     --timeout 10s
   ```

   El agente responde con el estado del plan. Luego el update v2:

   ```bash
   nats request 'keystone.edge-demo.cmd.apply' \
     "$(cat demo/plans/plan-v2.toml)" \
     --timeout 10s
   ```

4. **Suscríbete a los eventos de estado y salud** (otra terminal):

   ```bash
   nats sub 'keystone.edge-demo.events.>'
   ```

   Verás en tiempo real las transiciones `state:running`, `health:healthy`,
   restarts, etc. Esto es lo que una plataforma de flota consume.

Subjects relevantes (reemplaza `edge-demo` por tu `--nats-device-id`):

| Subject                              | Uso                               |
|--------------------------------------|-----------------------------------|
| `keystone.edge-demo.cmd.apply`       | Apply plan (payload = TOML)       |
| `keystone.edge-demo.cmd.stop`        | Stop plan                         |
| `keystone.edge-demo.cmd.status`      | Estado del plan                   |
| `keystone.edge-demo.cmd.graph`       | Grafo de dependencias             |
| `keystone.edge-demo.cmd.restart`     | Reinicio de componente            |
| `keystone.edge-demo.events.state`    | Eventos de estado publicados      |
| `keystone.edge-demo.events.health`   | Eventos de salud publicados       |

### Ruta rápida con MQTT (Mosquitto local)

1. **Arranca Mosquitto**:

   ```bash
   docker run --rm -d --name mosquitto -p 1883:1883 eclipse-mosquitto:2 \
     mosquitto -c /mosquitto-no-auth.conf
   ```

2. **Relanza el agente con MQTT**:

   ```bash
   ./keystone \
     --http :8080 \
     --mqtt-broker tcp://localhost:1883 \
     --mqtt-device-id edge-demo
   ```

3. **Aplica el plan publicando el TOML** en el topic `apply`:

   ```bash
   mosquitto_pub -h localhost \
     -t 'keystone/edge-demo/cmd/apply' \
     -f demo/plans/plan-v1.toml -q 1
   ```

4. **Observa eventos**:

   ```bash
   mosquitto_sub -h localhost -t 'keystone/edge-demo/events/#' -q 1
   ```

Topics relevantes:

| Topic                                  | Uso                         |
|----------------------------------------|-----------------------------|
| `keystone/edge-demo/cmd/apply`         | Apply plan (payload = TOML) |
| `keystone/edge-demo/cmd/stop`          | Stop plan                   |
| `keystone/edge-demo/cmd/status`        | Estado del plan             |
| `keystone/edge-demo/events/state`      | Eventos de estado           |
| `keystone/edge-demo/events/health`     | Eventos de salud            |
| `keystone/edge-demo/status` (LWT)      | `online` / `offline`        |

### Qué contar en esta parte de la demo

- **El mismo `Agent` y la misma `CommandHandler` detrás de los tres adaptadores.**
  Las recipes y el plan son idénticos. El adaptador solo cambia el transporte.
- **Los eventos MQTT/NATS son el mecanismo para monitorizar una flota remota**:
  lo que aquí vemos como logs en terminal, una plataforma los consume por
  subject/topic y los agrega.
- **LWT (Last Will & Testament) de MQTT** marca el dispositivo como `offline`
  si pierde conexión — clave para inventarios edge.
- **Para producción**: añadir TLS (`--nats-tls-ca`, `--mqtt-tls-ca`, …) y
  autenticación (NKey/creds en NATS, user/pass o mTLS en MQTT). Todo vía flags.

> Nota: los binarios de demo (`config`, `producer`, `consumer`) siguen
> expuestos por HTTP desde `keystoneserver :9000`. NATS/MQTT solo transportan
> el *control plane* (aplicar planes, recibir estado); la descarga de
> artefactos sigue siendo HTTP(S).
