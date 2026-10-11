# cbus2mqtt

A Go service with two main forwarding goroutines:

- C-Gate lighting status changes → JSON published to MQTT `cbus/<friendly_name>` (QoS 1, not retained). Clock date/time updates publish JSON to `cbus/<project>/<network>/<application>`. Other event lines remain on `cbus`.
- MQTT `cbus/<friendly_name>/set` → JSON commands resolved through `devices.yaml` and sent to C-Gate. Raw commands are also accepted on `cbus/set`. Replies are consumed and logged.

The MQTT library runs its own network goroutines. The command worker subscribes after receiving C-Gate's ready greeting.

Logs are written to stdout, including every incoming C-Gate event and command reply (including the greeting), every incoming MQTT command, and every outgoing MQTT publish with its topic and payload, as well as successful connections, command readiness, and connection failures.

Add `-v` to also log outgoing C-Gate commands and MQTT subscriptions. Retained or invalid commands are logged and remain ignored. Outgoing traffic is logged when attempted, not as confirmation of delivery.

Run with an existing MQTT broker and C-Gate server:

```sh
go run . -v -cgate-events localhost:20024 -cgate-commands localhost:20023 -mqtt tcp://localhost:1883
```

Use `-events-topic`, `-commands-topic`, and `-client-id` to override defaults. Both topic prefixes default to `cbus` and must have no wildcards. Device command topics end in `/set`; device state topics have no suffix. Set `MQTT_USERNAME` and `MQTT_PASSWORD` for broker authentication. C-Gate must permit this host through its access configuration and have the desired project/networks open.

The default read connection uses C-Gate's event port, **20024**. Port **20025** is a different stream: status changes rendered as commands and comments. Use `-cgate-events localhost:20025` if you specifically want that format. Neither stream echoes every command submitted by other clients; event output also depends on C-Gate's configured event levels. See the [C-Gate server guide](https://manualzz.com/doc/23261582/c-gate-server-guide). Each received line is logged as `C-Gate event received` before publishing it to MQTT.

For example, publish `on 1/56/1` to `cbus/set` to send that command to C-Gate. Payloads contain a single command, up to 4096 bytes; retained commands are ignored. QoS 1 can deliver duplicates, so prefer idempotent commands.

Publish `{"command":"ramp","value":255,"time":4}` to `cbus/bedroom/tv/set` to send `ramp //JERV/254/56/35 255 4` when that address has friendly name `bedroom/tv`. JSON commands support `on`, `off`, and `ramp`; ramp requires an integer `value` from 0 to 255 and accepts optional `time` in seconds (0 to 1020). The bridge subscribes to `cbus/#` and processes only topics ending in `/set`, preserves slashes in friendly names, and reloads `devices.yaml` for each command. Known address paths such as `cbus/JERV/254/56/35/set` also accept JSON. Unknown or ambiguous device topics and invalid payloads are logged and ignored. `-commands-topic` changes the base prefix. With `-v`, each outgoing C-Gate command is logged to stdout.

For lighting status changes such as `lighting on //JERV/254/56/22`, the bridge publishes `{"command":"on"}` to `cbus/living-room` when the group's name is `Living Room`. Names are lowercased and spaces become dashes; the friendly name replaces the entire address, including the project. On the first event for an address, the bridge sends `get //JERV/254/56/22 name` through the command connection (the project-qualified form of `get 254/56/22 name`). Discovered devices are saved to `devices.yaml` in the working directory and loaded on startup. Saved nonempty names take precedence over C-Gate lookups. Lookups and MQTT commands are serialized on that connection.

The file is a YAML array with exactly two fields per device:

```yaml
- address: //JERV/254/56/22
  friendly_name: living-room
- address: //JERV/254/56/23
  friendly_name: ""
```

Edit `friendly_name` while the bridge is running; the device list and all friendly names are reloaded before processing lighting events and before saving new devices. Custom names use the same topic normalization described above. Slashes are preserved as topic separators: `bedroom/tv` publishes to `cbus/bedroom/tv` for project `JERV`. All project-qualified addresses seen in events are added automatically, including clock applications, virtual devices, network addresses, and addresses in raw diagnostic events. Discovery accepts any number of address components and stores the full address. Devices without a discovered name have an empty `friendly_name`; successful later lighting name lookups fill empty entries. Existing nonempty names are preserved. A missing file is created automatically. Invalid files or failed writes stop the bridge with an error.

If a name lookup fails, returns an empty name, or contains MQTT wildcards or invalid control characters, the JSON is published to `cbus/JERV/254/56/22` instead; later events retry the lookup. Names equal to `set` or ending in `/set` use the address fallback because that suffix is reserved for commands. Names should be unique across all projects because groups with the same normalized name share a topic. Lighting events are split into `command`, optional numeric `value`, and optional numeric `time` in seconds. For example, `lighting ramp //JERV/254/56/22 0 4` becomes `{"command":"ramp","value":0,"time":4}`. Everything from the first `#` onward is discarded before parsing. Use `-cgate-events localhost:20025` for this status-change format; timestamped diagnostic events from port 20024 remain raw on the base topic. Subscribe to `cbus/#` to receive all event topics. `-events-topic` changes the base prefix.

The bridge does not generate Home Assistant discovery. It buffers up to 64 commands in memory. Connection errors, MQTT timeouts, or queue overflow stop the service with an error; use a process supervisor to restart it. Commands are not durably queued or retried. Ctrl-C and SIGTERM cancel both workers and close their sockets. Idle command-socket failures are detected when the next command is sent.

```sh
go test -race ./...
```

Protocol references: [Clipsal C-Gate command connection example](https://updates.clipsal.com/ClipsalSoftwareDownload/mainsite/cis/__data/page/4129/AN06-037-1_C-GateOnAMacOperatingSystem.pdf), [Eclipse Paho MQTT client](https://github.com/eclipse-paho/paho.mqtt.golang).

Clock events use the application address directly without a device name lookup. `clock date //JERV/254/223 2026-10-11 6` publishes `{"command":"date","value":"2026-10-11","day_of_week":6}` to `cbus/JERV/254/223`. `clock time //JERV/254/223 17:06:39 1` publishes `{"command":"time","value":"17:06:39","daylight_saving":1}` to the same topic. The [C-Gate server guide](https://manualzz.com/doc/23261582/c-gate-server-guide) defines day of week as 0–6 (Monday–Sunday) and daylight saving as 0 or 1. Dates and times remain strings; supplied flags are numeric, including zero.
