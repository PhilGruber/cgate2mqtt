# cbus2mqtt

A Go service with two main forwarding goroutines:

- C-Gate lighting status changes → JSON published to MQTT `cbus/events/<project>/<name>` (QoS 1, not retained). Other event lines remain on `cbus/events`.
- MQTT `cbus/commands` → raw command payloads sent to the C-Gate command socket with CRLF framing. Replies are consumed and logged.

The MQTT library runs its own network goroutines. The command worker subscribes after receiving C-Gate's ready greeting.

Logs are written to stdout, including successful MQTT and C-Gate connections, command readiness, and connection failures.

Add `-v` to also log every incoming C-Gate event, outgoing C-Gate command, MQTT subscription, accepted incoming MQTT command, and outgoing MQTT event publish. MQTT logs include topics and payloads; retained or invalid commands remain ignored. Outgoing traffic is logged when attempted, not as confirmation of delivery.

Run with an existing MQTT broker and C-Gate server:

```sh
go run . -v -cgate-events localhost:20024 -cgate-commands localhost:20023 -mqtt tcp://localhost:1883
```

Use `-events-topic`, `-commands-topic`, and `-client-id` to override defaults. Topics must be exact names without wildcards, and the command topic must be outside the event topic subtree. Set `MQTT_USERNAME` and `MQTT_PASSWORD` for broker authentication. C-Gate must permit this host through its access configuration and have the desired project/networks open.

The default read connection uses C-Gate's event port, **20024**. Port **20025** is a different stream: status changes rendered as commands and comments. Use `-cgate-events localhost:20025` if you specifically want that format. Neither stream echoes every command submitted by other clients; event output also depends on C-Gate's configured event levels. See the [C-Gate server guide](https://manualzz.com/doc/23261582/c-gate-server-guide). With `-v`, each received line is logged as `C-Gate event received` before publishing it to MQTT.

For example, publish `on 1/56/1` to `cbus/commands` to send that command to C-Gate. Payloads contain a single command, up to 4096 bytes; retained commands are ignored. QoS 1 can deliver duplicates, so prefer idempotent commands.

For lighting status changes such as `lighting on //JERV/254/56/22`, the bridge publishes `{"lighting":"on"}` to `cbus/events/JERV/living-room` when the group's name is `Living Room`. Names are lowercased and spaces become dashes; project spelling is preserved. On the first event for an address, the bridge sends `get //JERV/254/56/22 name` through the command connection (the project-qualified form of `get 254/56/22 name`). Successful names are cached in memory until restart. Lookups and MQTT commands are serialized on that connection.

If a name lookup fails, returns an empty name, or contains MQTT topic separators/wildcards, the JSON is published to `cbus/events/JERV/254/56/22` instead; later events retry the lookup. Names should be unique within a project because groups with the same normalized name share a topic. Any extra lighting arguments are kept in the JSON value, e.g. `lighting ramp //JERV/254/56/22 128 4` becomes `{"lighting":"ramp 128 4"}`. Use `-cgate-events localhost:20025` for this status-change format; timestamped diagnostic events from port 20024 remain raw on the base topic. Subscribe to `cbus/events/#` to receive all event topics. `-events-topic` changes the base prefix.

The bridge does not generate Home Assistant discovery. It buffers up to 64 commands in memory. Connection errors, MQTT timeouts, or queue overflow stop the service with an error; use a process supervisor to restart it. Commands are not durably queued or retried. Ctrl-C and SIGTERM cancel both workers and close their sockets. Idle command-socket failures are detected when the next command is sent.

```sh
go test -race ./...
```

Protocol references: [Clipsal C-Gate command connection example](https://updates.clipsal.com/ClipsalSoftwareDownload/mainsite/cis/__data/page/4129/AN06-037-1_C-GateOnAMacOperatingSystem.pdf), [Eclipse Paho MQTT client](https://github.com/eclipse-paho/paho.mqtt.golang).
