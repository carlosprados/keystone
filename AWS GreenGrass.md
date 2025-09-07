# AWS GreenGrass

## Introducción

En **AWS IoT Greengrass** (especialmente a partir de la versión 2.x), la instalación y actualización remota del software está diseñada para que no tengas que conectarte manualmente a cada dispositivo — se apoya en el propio framework de IoT Core y en el **Greengrass nucleus** para coordinar la operación.

Te lo desgloso paso a paso con detalles técnicos:

### Arquitectura Básica de actualización

En cada dispositivo donde está instalado Greengrass tienes:

- **Greengrass Nucleus**: el componente central que orquesta la ejecución y gestión de otros componentes.
- **Componentes**: paquetes funcionales que pueden ser de AWS, de terceros o tuyos propios.
- **AWS IoT Device Management** y/o **AWS IoT Jobs** para coordinar las actualizaciones desde la nube.

La actualización remota se basa en enviar una “orden” (Job) desde AWS al núcleo para que descargue e instale el nuevo paquete (o versión).

### Instalación Inicial remota

Normalmente la **primera instalación** de Greengrass Nucleus no se hace 100% remota si el dispositivo está “en blanco”.

- Tienes que hacer un **bootstrap** inicial: un script o imagen que incluya el instalador del nucleus y el registro en AWS IoT Core (certificados, política y thing).
- Esto se puede automatizar con **AWS IoT Fleet Provisioning** para evitar que tengas que generar credenciales manualmente.
- Una vez que el núcleo está registrado y en ejecución, ya puedes hacer todo lo demás sin tocar físicamente el equipo.

### Actualización Remota del Nucleus

- El **Nucleus** es también un componente (ID: `aws.greengrass.Nucleus`).
- Desde la consola o CLI puedes crear un **Deployment** apuntando al dispositivo o grupo de dispositivos, especificando la nueva versión del Nucleus.
- Greengrass ejecuta un **plan de despliegue**:
	1. Descarga el nuevo paquete desde Amazon S3 (gestionado por AWS).
	2. Valida la integridad con hashes y firmas.
	3. Sustituye binarios y scripts, manteniendo la configuración previa.
	4. Reinicia el núcleo para que tome la nueva versión.
- Puedes configurar **rolling updates** para flotas grandes, evitando que todos se actualicen a la vez.

### Actualización Remota de componentes

- Cada función, conector o servicio que tengas es un componente con su propio `recipe` y versión.
- Para actualizar:
	1. Publicas la nueva versión en AWS (con un `recipe.json` y el artefacto empaquetado).
	2. Creas un nuevo **deployment** apuntando a esa versión.
	3. El núcleo en el edge:
		- Recibe la notificación a través del canal MQTT seguro (IoT Core).
		- Descarga el artefacto (normalmente desde S3).
		- Sustituye la versión anterior y aplica cualquier migración de configuración si es necesaria.
- Si falla, el núcleo puede hacer **rollback automático** a la versión previa (esto depende de cómo definas la estrategia de actualización).

### Control y seguimiento

- Todo se orquesta desde **AWS IoT Jobs**.
- En la consola puedes ver:
	- Progreso por dispositivo.
	- Logs de instalación (stdout/stderr de cada paso).
	- Estado final (éxito, fallo, en progreso).
- Puedes habilitar que cada Greengrass suba logs al **CloudWatch Logs** o a S3 para auditoría.

### Buenas Prácticas para producción

- **Pruebas canary**: desplegar primero en un subconjunto controlado.
- **Versionado estricto** de tus componentes para poder hacer rollback rápido.
- **Aislamiento de red** controlado: el núcleo debe tener acceso saliente a IoT Core y S3 para descargar artefactos.
- **Control de dependencias**: si un componente depende de otro, ambos deben declararse en el `recipe`.
- **Automatización** con CI/CD: subir artefactos a S3, publicar el componente y lanzar el deployment vía AWS CLI o SDK.

## Ejecución de Nucleus

En Greengrass v2 el **Nucleus** no “es” Docker ni depende de él para funcionar. El Nucleus es el runtime central (Java) que orquesta componentes en el edge y puede ejecutarse de dos formas habituales en Linux:

1. **Como servicio del sistema (systemd)**
	- Durante la instalación puedes pasar `--setup-system-service true` para registrar el servicio `greengrass.service` y que arranque al boot. Se gestiona con `systemctl start|stop|status greengrass`. Requiere que el host use systemd. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/configure-installer.html?utm_source=chatgpt.com "Installer arguments - AWS IoT Greengrass"))
	- Alternativamente, puedes no crear el servicio y lanzar el loader directamente: `/greengrass/v2/alts/current/distro/bin/loader`. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/run-greengrass-core-v2.html?utm_source=chatgpt.com "Run the AWS IoT Greengrass Core software"))
2. **Dentro de un contenedor Docker (opcional)**
	- También puedes **encapsular el propio Nucleus** en un contenedor Docker si tu estrategia de operación lo prefiere, pero no es obligatorio. AWS documenta cómo construir/arrancar la imagen de Greengrass Core dentro de Docker. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/run-greengrass-docker.html?utm_source=chatgpt.com "Run AWS IoT Greengrass Core software in a Docker ..."))

### ¿Cómo Gestiona Nucleus el software de los componentes?

Greengrass v2 es **component-oriented**. Cada pieza de software (tu aplicación, agentes, conectores) es un **componente** con un *recipe* que define artefactos y **ciclos de vida** (install, startup, run, shutdown). El Nucleus ejecuta los comandos que declares en esos ciclos de vida en el propio host, por defecto como **procesos locales** (no containerizados). ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/component-recipe-reference.html?utm_source=chatgpt.com "AWS IoT Greengrass component recipe reference"))

- **Usuario del sistema**: esos procesos se ejecutan con el usuario/grupo que configures (típicamente `ggc_user:ggc_group`), lo que fija sus permisos de archivos, red, etc. Puedes establecerlo en la instalación o vía despliegue actualizando la configuración del componente `aws.greengrass.Nucleus`. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/develop-greengrass-components.html?utm_source=chatgpt.com "Develop AWS IoT Greengrass components"))
- **Límites de recursos**: puedes aplicar límites (CPU, memoria, etc.) por defecto y por componente a procesos no containerizados. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/greengrass-nucleus-component.html?utm_source=chatgpt.com "Greengrass nucleus"))

### ¿Y Docker para tus cargas?

Docker es **una opción** para **cómo** corre un componente, no un requisito del Nucleus.

- Si quieres que un componente **corra en contenedor**, añades la dependencia **Docker Application Manager** (`aws.greengrass.DockerApplicationManager`). Este componente permite a Greengrass descargar imágenes (ECR público/privado, Docker Hub) y gestionar credenciales para ECR privado. Tu recipe entonces invoca contenedores (incluso vía Docker Compose) en los pasos de lifecycle. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/docker-application-manager-component.html?utm_source=chatgpt.com "Docker application manager - AWS IoT Greengrass"))
- AWS proporciona guías y ejemplos de despliegue de **Docker Compose** como componentes Greengrass. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/run-docker-container.html?utm_source=chatgpt.com "Run a Docker container - AWS IoT Greengrass"), [mikelikesrobots.github.io](https://mikelikesrobots.github.io/blog/docker-compose-in-gg/?utm_source=chatgpt.com "Deploying Docker Compose with Greengrass!"))

### ¿Y Lambda en el edge?

Si importas **funciones AWS Lambda** como componentes, Greengrass usa el **Lambda manager** y **Lambda runtimes** para ejecutarlas localmente. Pueden correr en modo proceso o en contenedor según configuración y requisitos del runtime. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/lambda-manager-component.html?utm_source=chatgpt.com "Lambda manager - AWS IoT Greengrass"))

---

## Resumen operativo claro

- **Arranque y gestión del propio Nucleus**: normalmente **systemd** (`greengrass.service`) en Linux; también puede ejecutarse “a pelo” con el loader, o empaquetarse en **Docker** si lo prefieres. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/run-greengrass-core-v2.html?utm_source=chatgpt.com "Run the AWS IoT Greengrass Core software"))
	
- **Ejecución de tu software**: por defecto como **procesos del host** controlados por los *lifecycles* definidos en el **recipe** del componente y corriendo bajo `ggc_user[:ggc_group]`. **Docker es opcional** para componentes que decidas containerizar (vía `DockerApplicationManager`). ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/component-recipe-reference.html?utm_source=chatgpt.com "AWS IoT Greengrass component recipe reference"))

Si te interesa, puedo darte un **recipe mínimo** para cada modalidad (proceso nativo vs. Docker/Compose) y las **políticas/dependencias** necesarias para producción (usuario, límites, permisos de Docker, ECR, etc.).

## Lenguajes de programación de Nucleus

- **Hasta Greengrass v2.13** el *runtime* del dispositivo —el **Nucleus**— era **Java**. De hecho, la instalación exigía JVM (Corretto/OpenJDK 8+), y la documentación lo indicaba explícitamente. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/quick-installation.html?utm_source=chatgpt.com "Install AWS IoT Greengrass Core software with automatic ..."))
- **Desde v2.14 (diciembre de 2024)** AWS introdujo **un segundo runtime alternativo**, llamado **Greengrass Nucleus Lite**, **escrito en C**. No reemplaza al de Java; conviven dos implementaciones del Nucleus: la clásica en **Java** y la nueva en **C** para dispositivos con recursos muy limitados. ([Amazon Web Services, Inc.](https://aws.amazon.com/blogs/iot/aws-iot-greengrass-nucleus-lite-revolutionizing-edge-computing-on-resource-constrained-devices/?utm_source=chatgpt.com "AWS IoT Greengrass nucleus lite – Revolutionizing edge ..."), [AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/how-it-works.html?utm_source=chatgpt.com "How AWS IoT Greengrass works"))

### Qué significa en la práctica

1. **Dos nucleos posibles**
	- **Nucleus (Java):** máxima compatibilidad, soporte amplio de funcionalidades y ecosistema maduro; requiere JVM y tiene mayor huella de memoria. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/how-it-works.html?utm_source=chatgpt.com "How AWS IoT Greengrass works"))
	- **Nucleus Lite (C):** **más ligero**, pensado para hardware restringido. Implementa **un subconjunto** de la API v2; reduce dependencias (no requiere JVM), pero algunas capacidades avanzadas pueden no estar disponibles todavía. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/greengrass-nucleus-lite-component.html?utm_source=chatgpt.com "Greengrass nucleus lite"))
2. **Componentes y ejecución de cargas**
	- En ambos casos, tus **componentes** siguen definiéndose con *recipes* y **lifecycles** (install, run, shutdown). Pueden ejecutarse como procesos del host o, si lo configuras, en contenedores vía el **Docker Application Manager**. La elección de Java vs. C afecta al **runtime del Nucleus**, no a que “todo vaya en Docker” o similar. ([GitHub](https://github.com/aws-greengrass/aws-greengrass-nucleus?utm_source=chatgpt.com "aws-greengrass/aws-greengrass-nucleus"))
3. **Requisitos del sistema**
	- Si eliges el **Nucleus Java**, la instalación te pedirá **JRE/JDK** en el dispositivo. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/quick-installation.html?utm_source=chatgpt.com "Install AWS IoT Greengrass Core software with automatic ..."))
	- Con **Nucleus Lite (C)** no necesitas JVM; sigue aplicando que tus componentes pueden requerir sus propios *runtimes* (p. ej., Python) si así lo declaran. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/greengrass-nucleus-lite-component.html?utm_source=chatgpt.com "Greengrass nucleus lite"))

### Cuándo usar cada uno

- **Elige Java** si quieres **paridad funcional** con lo ya existente, compatibilidad con componentes complejos, e integraciones maduras (es el camino “seguro” para la mayoría de flotas actuales). La propia guía y el repo oficial siguen mostrando el núcleo principal en Java. ([GitHub](https://github.com/aws-greengrass/aws-greengrass-nucleus?utm_source=chatgpt.com "aws-greengrass/aws-greengrass-nucleus"))
- **Elige Lite (C)** si tu prioridad es **memoria/CPU** y tus casos de uso encajan con el **conjunto de funciones soportadas** por Lite hoy. Revisa la tabla de compatibilidad antes de migrar. ([AWS Documentation](https://docs.aws.amazon.com/greengrass/v2/developerguide/greengrass-nucleus-lite-component.html?utm_source=chatgpt.com "Greengrass nucleus lite"))

## Receipes

A continuación tienes **dos recetas completas** para Greengrass v2 en **YAML** (también se admiten en JSON). La primera ejecuta un binario “nativo” como proceso del host; la segunda levanta un contenedor vía **DockerApplicationManager**. Ambas usan buenas prácticas: versionado semántico, `runWith`, `configuration`, sustitución de variables y control de dependencias.

---

### 1) Componente “nativo” (proceso del host)

```yaml
RecipeFormatVersion: '2020-01-25'
ComponentName: com.wisewolf.hello
ComponentVersion: 1.0.0
ComponentType: aws.greengrass.generic
ComponentDescription: >
  Demo component that runs a local binary and prints heartbeats.
Publisher: Wisewolf Labs
ComponentDependencies:
  aws.greengrass.TokenExchangeService:
    VersionRequirement: '>=2.0.0 <3.0.0'
    DependencyType: HARD
# Valores de configuración por defecto, sobre-escribibles en el deployment
ComponentConfiguration:
  DefaultConfiguration:
    log_level: INFO
    interval_seconds: 30
    message: "Hello from the edge"
    # directorio de trabajo (sobre el que tendrá permisos ggc_user)
    work_dir: "/greengrass/v2/packages/artifacts/com.wisewolf.hello/1.0.0"
Manifests:
  - Platform:
      os: linux
      architecture: amd64
    Artifacts:
      - Uri: s3://my-artifacts-bucket/greengrass/com.wisewolf.hello/1.0.0/hello
        Unarchive: NONE
        Permission:
          Read: ALL
          Execute: ALL
          # El Nucleus ajusta a ggc_user:ggc_group, pero aquí acotamos permisos de ficheros
    Lifecycle:
      # Hook de instalación (opcional): preparar directorios, permisos, dependencias del SO, etc.
      Install:
        RequiresPrivilege: true
        Script: |-
          set -e
          echo "Installing com.wisewolf.hello {{ComponentVersion}}"
          chmod +x "{artifacts:decompressedPath}/hello"
      # Arranque: lanza el proceso como ggc_user (ver runWith más abajo)
      Run:
        Script: |-
          set -e
          LOG_LEVEL="{{configuration:/log_level}}"
          INTERVAL="{{configuration:/interval_seconds}}"
          MSG="{{configuration:/message}}"
          cd "{artifacts:decompressedPath}"
          ./hello --log-level "$LOG_LEVEL" --interval "$INTERVAL" --message "$MSG"
      # Parada: el Nucleus envía SIGTERM al proceso; si necesitas limpieza explícita:
      Shutdown:
        Script: |-
          echo "Shutting down com.wisewolf.hello"
    # Control de usuario/grupo del proceso y límites (si el Nucleus los tiene habilitados)
    RunWith:
      posixUser: "ggc_user:ggc_group"
      # Opcional: límites de recursos (si configurados en el Nucleus)
      # systemResourceLimits:
      #   memory: 128000000    # bytes
      #   cpu: 40              # porcentaje
```

**Puntos clave del ejemplo:**

- `Artifacts`: el binario `hello` se sirve desde S3 (puede ser también `s3://`, `https://` o `file://`).
- `ComponentConfiguration.DefaultConfiguration`: parámetros que podrás **sobrescribir por despliegue** sin cambiar la versión del componente.
- `Lifecycle.Install/Run/Shutdown`: ganchos de ciclo de vida. El comando `Run` **bloquea** y el Nucleus monitoriza su health.
- `RunWith.posixUser`: tu proceso **no** corre como root salvo que lo pidas explícitamente; mejor práctica es usar `ggc_user:ggc_group`.

---

### 2) Componente que ejecuta Docker/Compose

Este ejemplo levanta un servicio empaquetado como imagen Docker. Requiere el componente **`aws.greengrass.DockerApplicationManager`** instalado en el dispositivo.

```yaml
RecipeFormatVersion: '2020-01-25'
ComponentName: com.wisewolf.hello.docker
ComponentVersion: 1.0.0
ComponentType: aws.greengrass.generic
ComponentDescription: >
  Demo component that runs a container using DockerApplicationManager (optionally via Compose).
Publisher: Wisewolf Labs
ComponentDependencies:
  aws.greengrass.DockerApplicationManager:
    VersionRequirement: '>=2.0.0 <3.0.0'
    DependencyType: HARD
  aws.greengrass.TokenExchangeService:
    VersionRequirement: '>=2.0.0 <3.0.0'
    DependencyType: HARD
ComponentConfiguration:
  DefaultConfiguration:
    image: "public.ecr.aws/wisewolf/hello:1.0.0"
    container_name: "ww-hello"
    env:
      LOG_LEVEL: "INFO"
      INTERVAL_SECONDS: "30"
      MESSAGE: "Hello from Docker"
    # Si prefieres Compose, indica el nombre de archivo y servicio
    use_compose: false
    compose_file: "docker-compose.yml"
    compose_service: "hello"
Manifests:
  - Platform:
      os: linux
    Artifacts:
      # Solo es necesario si usas Compose y quieres versionar el compose file como artefacto
      - Uri: s3://my-artifacts-bucket/greengrass/com.wisewolf.hello.docker/1.0.0/docker-compose.yml
        Unarchive: NONE
        Permission:
          Read: ALL
    Lifecycle:
      Install:
        RequiresPrivilege: true
        Script: |-
          set -e
          echo "Preparing Docker-based component {{ComponentName}}:{{ComponentVersion}}"
          # Pullear imágenes aquí es opcional; también se puede hacer en Start
      Run:
        RequiresPrivilege: true
        Script: |-
          set -e
          IMAGE="{{configuration:/image}}"
          NAME="{{configuration:/container_name}}"
          LOG_LEVEL="{{configuration:/env/LOG_LEVEL}}"
          INTERVAL="{{configuration:/env/INTERVAL_SECONDS}}"
          MESSAGE="{{configuration:/env/MESSAGE}}"
          USE_COMPOSE="{{configuration:/use_compose}}"

          if [ "$USE_COMPOSE" = "true" ]; then
            cd "{artifacts:decompressedPath}"
            # Levantar con Compose (requiere docker compose instalado en el host)
            docker compose --file "{{configuration:/compose_file}}" pull "{{configuration:/compose_service}}" || true
            docker compose --file "{{configuration:/compose_file}}" up -d "{{configuration:/compose_service}}"
          else
            # Crear/actualizar contenedor de forma idempotente
            docker pull "$IMAGE" || true
            # Para actualización atómica: parar/eliminar si existe, luego recrear
            if docker ps -a --format '{{"{{.Names}}"}}' | grep -q "^$NAME$"; then
              docker rm -f "$NAME" || true
            fi
            docker run --name "$NAME" --restart=unless-stopped -e LOG_LEVEL="$LOG_LEVEL" -e INTERVAL_SECONDS="$INTERVAL" -e MESSAGE="$MESSAGE" -d "$IMAGE"
          fi
      Shutdown:
        RequiresPrivilege: true
        Script: |-
          set -e
          NAME="{{configuration:/container_name}}"
          USE_COMPOSE="{{configuration:/use_compose}}"
          if [ "$USE_COMPOSE" = "true" ]; then
            cd "{artifacts:decompressedPath}"
            docker compose --file "{{configuration:/compose_file}}" down
          else
            docker rm -f "$NAME" || true
          fi
    RunWith:
      posixUser: "ggc_user:ggc_group"
      # Para usar Docker se requiere privilegio; el flag RequiresPrivilege en los hooks ya lo marca.
```

**Notas operativas:**

- Para ECR privado, configura credenciales (o usa el **Token Exchange Service** con permisos adecuados y un helper de login).
- `RequiresPrivilege: true` en `Run/Shutdown` permite usar el socket Docker del host. Alternativa: crear una política de sudo específica y ajustar permisos del socket con mayor fineza.
- Si prefieres que Docker gestione reinicios, `--restart=unless-stopped` (como en el ejemplo). El Nucleus seguirá viendo el *run* como activo.

### Publicación del componente (CLI)

1. Guarda el YAML como `hello.yml` o `hello-docker.yml`.
2. Crea la versión del componente:

```bash
aws greengrassv2 create-component-version \
  --inline-recipe fileb://hello.yml
```

(Para el segundo, cambia el fichero; asegúrate de haber subido los artefactos a S3 previamente).

1. Despliegalo a un **Thing** o **Thing Group**:

```bash
aws greengrassv2 create-deployment \
  --target-arn arn:aws:iot:eu-west-1:123456789012:thing/MyCoreDevice \
  --components '{
    "com.wisewolf.hello":{"componentVersion":"1.0.0"}
  }'
```

### Compatibilidad con Nucleus “Lite” (C)

- Estos *recipes* funcionan igual porque la semántica se basa en **lifecycles** y **artifacts**.
- Verifica en tu entorno que las capacidades necesarias (p. ej., `RequiresPrivilege`, límites de recursos, Docker manager) están soportadas en la variante Lite que elijas. Si tu dispositivo es muy restringido, prioriza el **modo proceso nativo**.

## Nucleus gestiona el ciclo de vida de los componentes

El **ciclo de vida de los componentes** en AWS IoT Greengrass v2 lo gestiona **100 % el Nucleus**, no delega en `systemd` para arrancarlos, pararlos o monitorizarlos.

El matiz importante es que **systemd solo se usa para arrancar y mantener vivo el propio Nucleus** en el host, no para orquestar tus componentes.

### Cómo se reparte la responsabilidad

#### Nucleus y systemd

- En una instalación típica, el instalador de Greengrass registra el servicio `greengrass.service` en systemd.
- Esto asegura que **al boot del sistema** se lance el Nucleus, y si el proceso principal del Nucleus muere, systemd lo reinicia.
- systemd **no sabe** qué componentes tienes, qué scripts corren, ni su estado.

### 2. **Gestión Interna de componentes**

- Una vez que el Nucleus está activo, este interpreta los *recipes* de cada componente y ejecuta sus **ciclos de vida** (`Install`, `Run`, `Shutdown`…).
- El Nucleus mantiene un **supervisor interno**:
	- Lanza los procesos definidos en los scripts de `Run`.
	- Monitorea su estado (procesos hijos).
	- Reinicia si el *lifecycle* así lo define o si el componente tiene política de reinicio implícita (por ejemplo, en funciones Lambda configuradas como “long-lived”).
	- Ejecuta `Shutdown` cuando paras o actualizas el componente.
- Si un componente se cuelga o su proceso muere, **es el Nucleus quien decide** qué hacer (reintentar, marcar como fallo, hacer rollback), no systemd.

### 3. **Procesos Privilegiados y Docker**

- Si el *recipe* marca `RequiresPrivilege: true`, el Nucleus lanza el proceso con permisos elevados usando el usuario configurado (`ggc_user` o root).
- Si usas el **Docker Application Manager**, el Nucleus invoca el binario `docker` o `docker compose` del host, pero sigue controlando el ciclo de vida desde su supervisor.
- En este caso, Docker sí tiene su propio restart policy, pero eso es **independiente** de systemd y del propio Nucleus; se solapan si los configuras.

---

### Resumen claro de como se ejecutan Nucleus y componentes

- **systemd**: arranca y vigila el **Nucleus**.
- **Nucleus**: arranca, monitoriza, reinicia y actualiza **tus componentes** según sus *recipes*.
- No hay integración directa tipo “componentes como servicios systemd” a menos que tú mismo lo implementes en un script de `Run`.

## ¿Por Qué no todo basado en Docker?

Gran pregunta estratégica. Resumo primero la intuición y luego entro en profundidad: **Greengrass v2 prioriza un modelo “componentes como procesos” orquestados por el Nucleus** (con opción de contenedores cuando conviene) porque en el edge real —hardware heterogéneo, redes inestables, requisitos de latencia y acceso a dispositivos— **la contenedorización no siempre es el punto de partida óptimo**. Una arquitectura “Docker-first, containerd-only, escrita en Go” sería elegante y moderna, pero **reduciría el espacio de aplicabilidad** y complicaría casos críticos en campo.

A continuación, los motivos de diseño más sólidos:

### 1) Portabilidad extrema y huella mínima

- **Heterogeneidad de dispositivos**: en edge hay desde x86 con 16 GB RAM hasta ARMv7 con 128–512 MB. Exigir Docker/containerd eleva la **cota mínima de recursos** (daemon, cgroups, overlayfs, dependencias del kernel y userspace).
- **Huella y arranque**: procesos “a pelo” arrancan más rápido y consumen menos memoria que un runtime de contenedores + shim. En equipos con filesystem lento (eMMC/SD) el *cold start* de imágenes grandes penaliza.
- **Greengrass Lite (C)** empuja aún más esta idea: menos dependencias, más sitios donde cabe.

### 2) Determinismo operativo y control de ciclo de vida

- **Supervisor único**: el Nucleus controla `Install/Run/Shutdown` y política de reinicio. Si todo viviera “dentro de Docker”, tendrías **dos orquestadores** (Nucleus y Docker) con políticas potencialmente solapadas (¿reinicia Docker o reinicia Greengrass?).
- **Actualizaciones atómicas**: el plan de despliegue del Nucleus (validación de artefactos, hashes, orden de dependencias, rollback) funciona igual para binarios nativos o imágenes, sin atarte a semánticas específicas del registry o del *layering* de Docker.

### 3) Acceso directo a hardware y RT-adjacent

- Muchos casos IoT exigen **acceso a dispositivos** (GPIO, I²C, SPI, serie, CAN, drivers propietarios) o **bibliotecas nativas** (OpenCV con aceleración, TPU/NPU de borde). En contenedor, montar `/dev`, drivers y permisos **es viable pero más frágil** y específico del host.
- **Latencia y *jitter***: eliminar capas reduce el *jitter* en pipelines de datos o control. Para control casi-en-tiempo-real, cada capa importa.

### 4) Seguridad: principio de mínimo privilegio sin “todas las puertas abiertas”

- Con procesos nativos, el modelo de seguridad puede basarse en **usuarios/grupos dedicados (ggc_user)**, AppArmor/SELinux y *capabilities* puntuales.
- En contenedores **muchos equipos acaban ejecutando privilegiado** o con `--device` amplios para “que funcione”, diluyendo el beneficio. Greengrass, en cambio, te empuja a declarar exactamente **qué necesita cada componente**.

### 5) Redes difíciles y modo desconectado

- En campo hay **proxy, TLS interceptado, enlaces VSAT, 2G**, ventanas de conectividad cortas. Greengrass puede **cachear artefactos**, reintentar y **hacer *graceful degradation*** sin depender de la semántica de *pull* de imágenes y de los *manifests* OCI.
- Además, **artefactos no OCI** (scripts, modelos ML, ficheros de configuración) se tratan de forma nativa como *first-class citizens* en el recipe.

### 6) Compatibilidad con múltiples modelos de ejecución

- Greengrass **no te prohíbe Docker**: lo habilita vía `DockerApplicationManager` cuando aporta valor (aislamiento, empaquetado reproducible, portabilidad entre hosts homogéneos).
- También soporta **Lambda en el edge** y procesos nativos. Esa **poligamia de runtimes** amplía el abanico de casos de uso, en lugar de atarse a *containerd* como única vía.

### 7) Supervivencia operativa y observabilidad unificada

- Con un único supervisor (Nucleus) tienes **telemetría, logging, health y Jobs/Deployments** coherentes para todos los tipos de componente.
- Si el orquestador fuera “Docker-first”, **tu plano de observabilidad** se fragmenta (¿miramos CloudWatch/IoT Jobs o eventos de Docker/CRI?).

### 8) Compatibilidad hacia atrás y ecosistema existente

- El mundo Greengrass v1/v2, Lambda importada, managers oficiales (Kinesis, Secrets, StreamManager, etc.) y **componentes ya publicados** esperan el modelo de lifecycle de Greengrass. Cambiar el fundamento a *containerd* rompería o complicaría esa continuidad.

## ¿Sería “mejor” Una implementación Go + containerd?

Depende del “mejor” que persigas:

**Dónde brillaría:**

- Entornos **homogéneos y potentes** (por ejemplo, flotas x86_64 con 4–8 GB y kernel moderno) donde ya usas **OCI/OCI-registry**, *GitOps de contenedores* y necesitas ***namespaces* + cgroups** por defecto.
- **Suministro de *sidecars*** y herramientas *off-the-shelf* (Trivy, Syft, Notary, cosign) para **SBOM y firma** integradas en la cadena OCI.
- Integración con un **orquestador local** (k3s/microk8s) cuando quieres desplegar *charts* y CRDs en el edge y usar Greengrass “solo” como *control-plane* IoT.

**Dónde perderías:**

- **Dispositivos pequeños** y entornos con drivers ariscos.
- Complejidad operativa por el **doble bucle de orquestación** (Jobs/Deployments *vs.* políticas de reinicio de contenedores/compose).
- Flexibilidad de **artefactos no contenedor** tratados igual que ejecutables o modelos.

En síntesis: Go + containerd es una opción estupenda para **Edge “tipo mini-datacenter”**. Greengrass prioriza el **Edge “dispositivo primero”** y por eso **elige procesos nativos como estándar** y **contenedores como opción**.

---

## Patrón práctico recomendado (híbrido sensato)

1. **Por defecto**: componentes nativos con `RunWith` y límites de recursos; artefactos en S3; lifecycles claros.
2. **Containeriza** solo donde aporte valor tangible:
	- Librerías del sistema complejas/difíciles de empotrar,
	- Múltiples servicios con dependencias incompatibles,
	- Necesidad de empaquetado reproducible o SBOM OCI.
		
3. **Defensa en profundidad**:
	- Usuarios/grupos dedicados, *capabilities* mínimas, *seccomp/AppArmor* si procede,
	- Evita `--privileged`; monta solo los `/dev` imprescindibles,
	- *Healthchecks* en el propio componente (no delegar solo en restart policy del contenedor).
4. **Observabilidad única**: métricas y logs canalizados por Greengrass (y, si hay contenedores, *exporters* livianos).

---

## Decisión de arquitectura en una frase

Greengrass escogió **control centralizado, dependencias mínimas y compatibilidad amplia**; **contenedores como herramienta, no como axioma**. En edge real eso reduce fricción, acelera el *time-to-value* y evita quedarte fuera en el 20–30 % de dispositivos que, sencillamente, **no pueden** o **no deben** llevar un runtime de contenedores permanente.

Si quieres, preparo un **ADR (Architecture Decision Record)** con pros/cons, criterios de selección (RAM/CPU, kernel, drivers, criticidad, latencia, conectividad), y matriz de decisión para “proceso nativo” vs “contenedor” por tipo de componente de tu flota.
