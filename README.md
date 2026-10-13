# cbus2mqtt

A Go service with two main forwarding goroutines:

- C-Gate lighting, measurement, temperature and security status → retained MQTT JSON (QoS 1), using friendly names or full address paths. Clock date and time use separate retained topics. Unrecognised event lines remain on `cbus` without retention.
- MQTT `cbus/<friendly_name>/set` → JSON commands resolved through `devices.yaml` and sent to C-Gate. Supported raw device operations are also accepted on `cbus/set`. Replies are consumed, logged and published as command results.

The MQTT library runs its own network goroutines. The command worker subscribes after receiving C-Gate's ready greeting.

Logs are written to stdout, including every incoming C-Gate event and command reply (including the greeting), every incoming MQTT command, and every outgoing MQTT publish with its topic and payload, as well as successful connections, command readiness, and connection failures.

Add `-v` to also log outgoing C-Gate commands and MQTT subscriptions. Retained or invalid commands are logged and remain ignored. Outgoing traffic is logged when attempted, not as confirmation of delivery.

Run with an existing MQTT broker and C-Gate server:

```sh
go run . -v -cgate-events localhost:20024 -cgate-commands localhost:20023 -mqtt tcp://localhost:1883
```

Use `-events-topic`, `-commands-topic`, and `-client-id` to override defaults. Both topic prefixes default to `cbus` and must have no wildcards. Device command topics end in `/set`; lighting and sensor state topics have no suffix. Security status categories and clock fields have separate topic suffixes. Set `MQTT_USERNAME` and `MQTT_PASSWORD` for broker authentication. C-Gate must permit this host through its access configuration and have the desired project/networks open.

All options can also be set in `/etc/cbus2mqtt/cbus2mqtt.conf`, using flag names without leading dashes:

```ini
v=false
cgate-events=localhost:20025
cgate-commands=localhost:20023
mqtt=tcp://localhost:1883
client-id=cbus2mqtt
events-topic=cbus
commands-topic=cbus
mqtt-username=
mqtt-password=
retain-state=true
startup-state=true
startup-networks=
```

The file is optional. Command-line flags override file settings; file settings override built-in defaults and the `MQTT_USERNAME`/`MQTT_PASSWORD` environment variables. Credentials can also be set with `-mqtt-username` and `-mqtt-password`. Blank lines and lines starting with `#` are ignored. Whitespace around keys and values is trimmed; values are literal, without quotes or inline comments, and may contain `=` or `#`. Use `v=true` or `v=false` for verbose logging. Unknown keys, malformed lines, invalid values, and unreadable files stop startup with an error. Restrict file permissions if it contains credentials, while allowing the service user to read it.

The Debian package installs the defaults to `/usr/share/cbus2mqtt/defaults/cbus2mqtt.conf` and creates `/etc/cbus2mqtt/cbus2mqtt.conf` only if it is missing. Installation and upgrades preserve existing config files, including empty files and symlinks.

The default read connection uses C-Gate's status-change port, **20025**, where application events are rendered as commands and comments. Use `-cgate-events localhost:20024` for the structured event stream. Both formats are supported. Neither stream echoes every command submitted by other clients; event output also depends on C-Gate's configured event levels. See the [C-Gate server guide](https://manualzz.com/doc/23261582/c-gate-server-guide). Each received line is logged as `C-Gate event received` before publishing it to MQTT.

For example, publish `on 1/56/1` to `cbus/set` to send that command to C-Gate. Payloads contain a single command, up to 4096 bytes; retained commands are ignored. QoS 1 can deliver duplicates, so prefer idempotent commands.

Publish `{"command":"ramp","value":255,"time":4}` to `cbus/bedroom/tv/set` to send `ramp //JERV/254/56/35 255 4` when that address has friendly name `bedroom/tv`. Lighting JSON commands support `on`, `off`, `ramp`, `get_brightness`, and `terminate_ramp`; ramp requires an integer `value` from 0 to 255 and accepts optional `time` in seconds (0 to 1020). The bridge subscribes to `cbus/#` and processes only topics ending in `/set`, preserves slashes in friendly names, and reloads `devices.yaml` for each command. Fully qualified numeric address paths such as `cbus/JERV/254/56/35/set` also accept JSON, even before discovery. Unknown or ambiguous friendly names and invalid payloads produce an error on the corresponding `/result` topic and never reach C-Gate. `-commands-topic` changes the base prefix. With `-v`, each outgoing C-Gate command is logged to stdout.

For lighting status changes such as `lighting on //JERV/254/56/22`, the bridge publishes `{"type":"lighting","command":"on"}` to `cbus/living-room` when the group's name is `Living Room`. Names are lowercased and spaces become dashes; the friendly name replaces the entire address, including the project. On the first event for an address, the bridge sends `get //JERV/254/56/22 name` through the command connection (the project-qualified form of `get 254/56/22 name`). Discovered devices are saved to `devices.yaml` in the working directory and loaded on startup. Saved nonempty names take precedence over C-Gate lookups. Lookups and MQTT commands are serialized on that connection.

The file is a YAML array with exactly two fields per device:

```yaml
- address: //JERV/254/56/22
  friendly_name: living-room
- address: //JERV/254/56/23
  friendly_name: ""
```

Edit `friendly_name` while the bridge is running; the device list and all friendly names are reloaded before processing lighting events and before saving new devices. Custom names use the same topic normalization described above. Slashes are preserved as topic separators: `bedroom/tv` publishes to `cbus/bedroom/tv` for project `JERV`. All project-qualified addresses seen in events are added automatically, including clock applications, virtual devices, network addresses, and addresses in raw diagnostic events. Discovery accepts any number of address components and stores the full address. Devices without a discovered name have an empty `friendly_name`; successful later lighting name lookups fill empty entries. Existing nonempty names are preserved. A missing file is created automatically. Invalid files or failed writes stop the bridge with an error.

If a name lookup fails, returns an empty name, or contains MQTT wildcards or invalid control characters, the JSON is published to `cbus/JERV/254/56/22` instead; later events retry the lookup. Names equal to `set` or ending in `/set` use the address fallback because that suffix is reserved for commands. Names should be unique across all projects because groups with the same normalized name share a topic. Event JSON includes the C-Bus application `type` (`lighting`, `measurement`, `temperature`, `security`, or `clock`). Lighting events are split into `command`, optional numeric `value`, and optional numeric `time` in seconds. For example, `lighting ramp //JERV/254/56/22 0 4` becomes `{"type":"lighting","command":"ramp","value":0,"time":4}`. Comment metadata contributes `source_unit` when present. Commented `#security` events are parsed as security status. Structured events from port 20024 preserve the original C-Gate `timestamp` string and `source_unit`, and decode application events and lighting level advice. Other diagnostic events remain raw on the base topic. Subscribe to `cbus/#` to receive all event topics. `-events-topic` changes the base prefix.

The bridge does not generate Home Assistant discovery. It buffers up to 64 commands in memory. Connection errors, MQTT timeouts, or queue overflow stop the service with an error; use a process supervisor to restart it. Commands are not durably queued or retried after transport failure. Ramp termination retries the alternate spelling `terminateramp` only if C-Gate rejects `terminate_ramp` with a syntax error. Ctrl-C and SIGTERM cancel both workers and close their sockets. Idle command-socket failures are detected when the next command is sent.

```sh
go test -race ./...
```

Protocol references: [Clipsal C-Gate command connection example](https://updates.clipsal.com/ClipsalSoftwareDownload/mainsite/cis/__data/page/4129/AN06-037-1_C-GateOnAMacOperatingSystem.pdf), [Eclipse Paho MQTT client](https://github.com/eclipse-paho/paho.mqtt.golang).

Clock events use the application address directly without a device name lookup. `clock date //JERV/254/223 2026-10-11 6` publishes `{"type":"clock","command":"date","value":"2026-10-11","day_of_week":6}` to `cbus/JERV/254/223/date`. `clock time //JERV/254/223 17:06:39 1` publishes `{"type":"clock","command":"time","value":"17:06:39","daylight_saving":1}` to `cbus/JERV/254/223/time`. The [C-Gate server guide](https://manualzz.com/doc/23261582/c-gate-server-guide) defines day of week as 0–6 (Monday–Sunday) and daylight saving as 0 or 1. Dates and times remain strings; supplied flags are numeric, including zero.

Device status and control are limited to end-user operations. The bridge does not commission devices, change their programming, manage projects, or implement scene/trigger control. `cbus/set` accepts only validated `on`, `off`, `ramp`, ramp termination, the supported status `get` parameters, `getstate`, read-only `tree`, and the security operations listed below. Relative raw addresses rely on C-Gate's existing default project; use full `//PROJECT/network/...` addresses for query replies to also publish device state.

Publish these JSON payloads to `cbus/<friendly_name>/set` or `cbus/<project>/<network>/<application>[/<device>[/<channel>]]/set`. Optional `type` must match the application's address. Lighting includes applications 48–95, heating 136 and room control 38; temperature is 25, measurement 228, and security 208.

| Device/application | JSON command | Effect |
|---|---|---|
| Lighting group | `{"command":"get_brightness"}` | Query the current brightness; publish `{"type":"lighting","command":"brightness","value":128}` for brightness 128. |
| Lighting group | `{"command":"terminate_ramp"}` | Stop dimming at the current brightness. Both C-Gate command spellings are handled. |
| Measurement channel | `{"command":"get_data"}` | Read the last cached measurement. Requires a device/channel address, e.g. `JERV/254/228/1/2`. |
| Temperature group | `{"command":"get_temperature"}` | Read the last cached Celsius temperature, e.g. `JERV/254/25/1`. |
| Security application | `{"command":"get_arm_state"}` | Read the cached panel armed state. |
| Security application | `{"command":"status_request","value":1}` | Request status report 1; use value 2 for report 2. Reports arrive asynchronously on the event connection. |
| Security application | `{"command":"arm","mode":"night"}` | Arm the panel; modes are `away`, `night`, `day`, `vacation`, and `highest`. Support depends on the panel. |
| Security zone | `{"command":"request_zone_name"}` | Request a zone name; its response arrives as an event. |

The documented protocol has no generic disarm command. The bridge does not emulate panel-specific keypad sequences or fabricate a disarm operation. Control acknowledgements report C-Gate acceptance; actual device state comes from events or read queries.

`measurement data //JERV/254/228/1/2 235 -1 0` publishes a scaled `value` of 23.5, `unit` of `°C`, and the original `mantissa`, `exponent`, and numeric `unit_code`, with `type=measurement` and `command=data`. Standard unit codes are named; unknown codes remain numeric. `temperature broadcast //JERV/254/25/1 18.5` publishes `type=temperature`, `command=broadcast`, `value=18.5`, and `unit=°C`. Cached measurement queries return an error if C-Gate indicates that no reading has been received; the bridge does not publish a fabricated zero.

Security events publish under the device topic plus `/armed`, `/alarm`, `/alarm-type`, `/tamper`, `/panic`, `/battery`, `/charging`, `/mains`, `/ready`, `/entry-delay`, `/exit-delay`, `/password`, or `/zone-name`. Zone events use the zone address/friendly name plus `/zone`; the `command` distinguishes sealed, unsealed, open, short and isolated. `/armed` has numeric `value` 0 for disarmed, 1 for fully armed, 2 for partially armed, or a panel-specific number. Other state categories use boolean values where applicable. These independent retained topics prevent unrelated status updates from overwriting one another. Reports use `/status/1` and `/status/2`: report 1 includes `armed_state`, `tamper_state`, `panic_state`, `zone_start=1` and 32 numeric zone states; report 2 has `zone_start=33` and 48 states. Zone codes are 0 sealed, 1 unsealed, 2 open and 3 short. Reports retain their complete snapshot separately from individual status events.

Every non-retained MQTT command receives a non-retained result at its command topic with `/set` replaced by `/result`. Results include `ok`, the generated C-Gate `command`, response `code` and `replies`, and `error` on failure. For example, `cbus/room/set` receives its result at `cbus/room/result`. Invalid requests produce an error result without sending a command. Retained requests are always ignored. Query results additionally update the device's state topic. Subscribe to results before sending commands; they are not stored or replayed.

With `startup-state=true` (default), startup queries all supported devices saved in `devices.yaml`, requests both reports for known security panels, and runs read-only `TREE` and `GETSTATE` on known networks. `TREE` supplies lighting brightness directly; `GETSTATE` snapshots arrive on port 20024 when that stream is selected. Unknown/unsynchronised brightness values and measurements are not published as valid state. Set `startup-networks=//JERV/254,//OTHER/1` to include networks even on a first run with an empty registry. Explicit address commands work for groups discovered in a network snapshot; friendly names require registry entries. Startup results appear at `<events-topic>/startup/result`. Set `startup-state=false` to disable automatic queries.

Device state is retained by default; `retain-state=false` disables state retention. Clock state uses separate `/date` and `/time` topics so neither overwrites the other. Unrecognised events and command results are never retained. Retained values represent the last observed state; use bridge availability to determine whether updates are currently live.

The bridge publishes retained `offline`/`online` at `<events-topic>/bridge/<client-id>/availability`. It starts offline, becomes online after C-Gate is ready and startup commands finish, and publishes offline on shutdown. An MQTT Last Will publishes offline after an unexpected disconnect. C-Gate command rejection does not stop the bridge; transport failures, MQTT failures, invalid event data, or queue overflow do.

Security `status_request` events publish JSON with `type=security`, `command=status_request`, report number in `value`, and `source_unit` when present. Their topic is `<device-topic>/status/request/1` or `/status/request/2`, using a saved friendly name or the full address. Request events are not retained and do not overwrite retained status reports.

Lighting MQTT queries use `{"command":"get_brightness"}`; returned state uses `{"type":"lighting","command":"brightness","value":128}` with the existing 0–255 scale. Raw MQTT queries may use `get //JERV/254/56/29 brightness`. The bridge translates these to C-Gate `GET ... level`; protocol replies and diagnostic logs retain C-Gate terminology. `get_level` and raw `get ... level` remain accepted as compatibility aliases. Ramp payloads continue to use `value`.
