+++
title = "Control planes"
weight = 50
+++

# Control planes

Three ways to drive the same agent. They are **adapters** over one shared
`CommandHandler`, so any of them can do anything the others can — no feature drifts
between transports.

| Adapter | Best for | On by default |
|---|---|---|
| [HTTP](http/) | Local management, debugging, Prometheus | yes (loopback) |
| [NATS](nats/) | Large fleets, cloud, offline queueing with JetStream | no |
| [MQTT](mqtt/) | IoT platforms, constrained devices, existing brokers | no |

You can run several at once — HTTP on loopback for local debugging while MQTT
carries fleet commands is a common combination:

```bash
keystone --http 127.0.0.1:8080 \
         --mqtt-broker tls://broker.acme.com:8883 --mqtt-device-id edge-001
```

{{% children type="flat" depth="1" description="true" %}}
