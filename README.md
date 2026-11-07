# lmh-can

CAN and RS485 battery monitor for the LMH edge device (Seeed reComputer Industrial R21xx). It reads a BMS through the built-in SocketCAN ports or the on-board RS485 ports, decodes it into a typed, normalized state, and publishes JSON to MQTT.

- CAN is receive only: the driver never transmits on the bus. RS485 is request / response, so the LMH polls the BMS (Modbus RTU read requests only; it never writes registers). `sniff` is receive only.
- Standard library only (including a small built-in MQTT client). One static binary, cross-compiles for `linux/arm64`.
- Replaces the earlier SLCAN-based Go tool. Protocols are decoded by per-protocol decoders selected from a config file at run time.

## Status: what works and what does not

**Tested:** unit tests (decoders against frames captured from a LYTVOLT S15000 in every CAN mode it offers (`internal/protocol/testdata`), config validation, the engine's stale/ready logic, the MQTT client against a fake broker, candump parsing) pass with `go test -race ./...`. `replay` was run against the real capture.

RS485 (PACE Modbus) was tested on a pseudo-terminal against a fake PACE slave, which exercises the real termios and Modbus code. The sniffer was tested the same way.

**Not tested on hardware yet:** the RS485 path on the LMH (`poll`, `sniff`, `run` with an `rs485` section), the SocketCAN reader on the LMH, the interface check (parses `ip -details -json`), the systemd units, and the built-in MQTT client against a real broker. Run `lmh-can run -stdout` on the LMH first.

### Protocols

`lmh-can protocols` prints this table. Names are the BMS's own CAN protocol names; `jin_lang`, `senergyinv` and `tbb_lithium` are accepted as aliases.

| Level | Protocols |
|---|---|
| **verified** (compared live with PbmsTools) | pylontech |
| **captured** (decoder written from, and tested against, a capture of the BMS in that mode) | goodwe, growatt, studer, srd, victron, victron23, luxpower, schneider, sma, sofar, pv, jinlang, senergy, tbb, megarevo |
| **unsupported** (no capture or specification yet) | deye, gigacity, afore, sorotec, must, solis |

"Captured" means: no vendor specification was available. Frame meanings were worked out from the capture and cross-checked against what is known about the battery (all 14 captured dialects agree on voltage, current, SOC, temperature and limits; Growatt and Schneider cycle counts are 50 as the RS485 reader shows; Schneider's pack serial equals the RS485 serial; Growatt's 16 cell voltages are in range). It has **not** been compared with a live inverter of that brand. Fields that could not be identified are left undecoded; alarm and protection bits are always reported raw.

Frames not decoded, per protocol (visible with `lmh-can replay`):

| Protocol | Not decoded |
|---|---|
| goodwe, studer | 0x371, 0x380, 0x381 |
| growatt | 0x312, 0x320, 0x323 |
| victron | 0x370, 0x372, 0x374-0x377 |
| victron23 | 0x360, 0x370-0x372, 0x374-0x378, 0x37A-0x37F, 0x380, 0x381 |
| schneider | 0x320, 0x325, 0x333 |
| sma | 0x35B |
| sofar, pv | 0x30F |
| jinlang | 0x71C |

Notes from the captures:
- The old tool's Studer frames (0xB0/0xC0/0xC1) do not appear: this BMS sends Goodwe-style frames in Studer mode.
- Some modes report no discharge voltage limit (goodwe, studer) or no SOH (srd). Those fields are omitted, not zero.
- Victron-style temperatures (0x373) are whole kelvin, so they are only accurate to about 1 K.
- Several unsupported names may be RS485-only protocols on this BMS; they are not in its CAN protocol list.

## RS485

The BMS has one RS485 protocol per mode, chosen in PbmsTools. `lmh-can protocols` lists them:

| Level | RS485 protocols |
|---|---|
| **verified** (register map checked live with the Python reader) | pacemodbus |
| **captured** (decoder written from, and tested against, replies of this BMS) | pylon, wow, voltronic, luxpower_vo3 |
| **unsupported** (no specification or capture yet) | growatt, se, pylon_f |

`pacemodbus` is PACE BMS Modbus RTU V1.3, function 3: registers 0-39 every poll (current, voltage, SOC/SOH, capacities, cycles, 16 cell voltages, temperatures, limits, warning / protection / status flags decoded by name) and the version / serial strings once. The Go port was checked against the V1.3 register layout with synthetic registers; the first run against the real BMS on the LMH has not been done.

Differences from CAN: RS485 protocols are polled, and the BMS answers only the address it is set to (default 1). The BMS must be set to the matching protocol in PbmsTools for the RS485 port you connect to; check the BMS manual for which of its ports carries which protocol.

`pylon` is the Pylontech RS485 protocol (ASCII frames, VER 20). It was written from `probe` replies of this BMS, not from the vendor document. Per poll it sends 0x42 (analog values: 16 cell voltages, 5 temperatures, current, voltage, remaining and total capacity, cycles) and 0x92 (charge / discharge voltage and current limits, flags byte); manufacturer (0x51) and serial (0x93) are read once. Things to know:
- The BMS answers **address 2 only** (`slave` = the ADR byte, default 2 for pylon).
- There is no SOC field in the reply: `soc_pct` is derived as remaining / total capacity (98.6 for 275.88 / 279.91 Ah), so it is not an integer like the BMS's own display.
- `temps_c` lists all five sensors in protocol order. Which is which (cell, MOSFET, ambient) is not identified, so none is mapped to `temperature_c` or the cell fields.
- Current is read as signed 0.1 A, positive = charging; it matches the earlier PACE / CAN readings of -0.7 A.
- The flags byte of 0x92 is reported raw as `request_flags_raw` (`40` in the capture).
- The serial from 0x93 is cut at 16 characters (`GPSR1PC200LS2805`; the PACE Modbus serial is the full 20).
- Alarm info (0x44) and system parameters (0x47) got no reply from this BMS, so alarms / protections are not available in this mode.

`wow` is Modbus RTU (slave 1, 9600 8N1 answered). A register dump of the BMS in WOW mode shows that registers 0-39 are the **same block as the PACE Modbus map with the PACE scales** (current 0.01 A, cells in mV, limits 0.1 V / 0.1 A), so the decoder reuses it. Cross-checks against Pylon replies from the same BMS: the cells add up to the pack voltage, full capacity 279.91 Ah, 50 cycles, current about -0.7 A, charge limit 54.4 V, charge current limit 0 A, discharge limit 200 A. Notes:
- The vendor's "WOW Comprehensive Modbus Map" PDF is **wrong for this BMS**: it gives current 0.1 A and cells 0.01 V, and its "limit" registers 62 / 70 / 78 / 81 hold protection thresholds in other units (58400 at address 61 is 58.4 V in mV, not 584 V). Do not decode from the PDF.
- Addresses 60-109 look like the BMS's protection parameter table (for example pack over-voltage 57.0 / 58.4 / 54.4 V, cell over-voltage 3.64 / 3.65 / 3.40 V, pack under-voltage 47.0 / 44.0 / 46.0 V, cell under-voltage 2.6 / 2.5 / 2.9 V); they are not decoded.
- The flag registers (9, 10, 11) are reported raw (`alarm_raw`, `protection_raw`, `status_raw`) without names, because their bit meanings in this mode are not confirmed. The raw values in the dump were 0000, 8000 and 0E00.
- Version and serial strings are not read in this mode.

### Wiring and bench test

On the LMH tested, **RS485-3 (A3 / B3 / GND3) is `/dev/ttyACM1`**. The ports are isolated: connect GND as well as A and B (a missing GND corrupted frames earlier). If you get no reply, swap A and B. The on-board ports do not need (and do not support) the Linux RS485 ioctl; direction is handled in hardware.

```
lmh-can poll /dev/ttyACM1 -device battery -protocol pacemodbus -once   # one read, indented JSON (9600 8N1, address 1)
lmh-can poll /dev/ttyACM1 -device battery -protocol pylon -once          # Pylon: address defaults to 2
lmh-can poll /dev/ttyACM1 -device battery -protocol pacemodbus -baud 9600 -slave 1     # JSON line every second
```

Device names such as `/dev/ttyACM1` can change between boots. For a permanent setup use a stable name from `ls -l /dev/serial/by-path/` as the `port` in the config.

### Capturing an unsupported protocol

## Inverters (RS485)

Inverters are a second device class (`"class": "inverter"` in the config; the wizard asks "What are you connecting? Inverter"). They are polled over RS485 only, publish to the same `<topic>/state` and `<topic>/status` pair as batteries, but with an inverter record (battery side, PV inputs, grid, output, load, temperatures, faults, energy counters). `lmh-can protocols` lists them under "RS485 inverters"; Batteries and inverters are kept completely separate: each has its own protocol list, and the kind is never guessed, from the device or from the protocol name. In the config it is the required `class` field (`battery` or `inverter`); on the command line it is the required `-device battery|inverter` flag of `poll` (and of `probe -style`), and a protocol is looked up only in that device's list, so a name can exist in both lists without clashing. `-device battery -protocol srne` is refused with a hint to use `-device inverter`.

### SRNE (unverified)

From "Energy storage inverter MODBUS communication protocol V1.7" (also used by the HF / HFP series, checked against the HFP4850S80-H manual). Standard Modbus RTU, 9600 8N1, function 3, at most 32 registers per read, register addresses in hex, 32-bit values low word first.

Wiring and settings on the HFP series (from its manual):
- Use the RJ45 RS485 port: pin 7 = A, pin 8 = B, pin 2 = GND. Pin 1 is a 5 V supply: do not connect it to the LMH. RS485-1 is meant for the WiFi / GPRS module; RS485-2 is the PC / telecom port.
- In the inverter's setup menu, set RS485-2 communication to **[32] SLA** (default), not **[32] BMS**. In BMS mode the port expects a battery, not a master.
- The inverter's RS485 address is menu parameter 14 on the display (default 1). Pass another one with `-slave`.

Each poll reads three blocks (`0x100` battery / PV / charge, `0x200` faults, state, grid, output, load, `0x220` heat sink temperatures) plus the energy counters (`0xF02D`, `0xF046`; a block the inverter rejects is not asked again) and, once, versions, address, model code and serial. A full cycle takes about one to two seconds because of the Modbus gap between requests. The second PV input is published only once it has reported a non-zero value. Fault codes are named from the HFP manual's table (codes not in it are shown as numbers). Not decoded: settings (`0xE000` and up), the 7-day history, fault history, phase B / C values.

First test, with the inverter connected and nothing else on the port:

    lmh-can probe /dev/ttyUSB4 -device inverter -style srne -out srne_probe.txt
    lmh-can poll /dev/ttyUSB4 -device inverter -protocol srne -once

### Voltronic (captured)

Implemented from Voltronic's "Inverter and BMS 485 communication Protocol" (2020) and checked on the BMS (its voltage, capacities and limits match the Pylon and PACE readings). The frames look like Modbus RTU but the reply has a 2-byte word-count length, so it has its own client. Slave 1 is the common address. To test: set Voltronic on the BMS in PbmsTools, then

    lmh-can probe /dev/ttyUSB4 -device battery -style voltronic -out voltronic_probe.txt
    lmh-can poll /dev/ttyUSB4 -device battery -protocol voltronic -once

The document says cell voltages are 0.1 V, and the BMS really does report them that way (33 = 3.3 V), so cells read as 3300 mV flat and no min / max / spread is published. The scale is still checked against the module voltage in case another firmware uses mV. Current is charge minus discharge current; "charge_disabled" in status is the BMS asking the charger to stop (its charge current limit is 0 A). Alarm and protection words are decoded with the document's bit names and also published raw; they have only been seen at zero.

### Luxpower_VO3 (captured)

Implemented from "Communication Protocol between Luxpowertek Hybrid inverter and battery, RS485" V0.3. It is Modbus function 3 at 9600 8N1, slave 1, 16 registers from address 0 (charge voltage and current limits, SOC / SOH, full capacity, voltage, current, status bits, fault and warning codes, max / min cell mV, max / min cell temperature, parallel number, cycles), but little-endian: the request is `01 03 00 00 10 00` plus CRC, and each data word in the reply is low byte first, so it has its own client. Checked on the BMS: it answers this request and rejects the standard big-endian one with exception 3; its values match the other protocols (limits 54.4 V / 0 A / 200 A, SOC, voltage). Full capacity is whole Ah only (279 for 279.91). Fault and warning codes are published raw (the document gives no bit meanings) and have only been seen at zero. To test: set Luxpower_VO3 on the BMS in PbmsTools, then

    lmh-can probe /dev/ttyUSB4 -device battery -style luxpower -out lux_probe.txt
    lmh-can poll /dev/ttyUSB4 -device battery -protocol luxpower_vo3 -once

`probe -style luxpower` also sends the standard big-endian request for comparison, in case this firmware answers that instead.

The remaining protocols (growatt, se, pylon_f) need their frames recorded. Set the protocol on the BMS in PbmsTools, connect the LMH **in parallel** with the inverter on the RS485 lines (A to A, B to B, GND), keep the stub short, and record while the inverter is polling:

```
lmh-can sniff /dev/ttyACM1 -baud 9600 -seconds 120 -out pylon_rs485.txt
```

Each line is one burst of bytes (normally a request or a reply) with its time, length, hex and ASCII. `-gap` (ms of silence that ends a burst, default 15) can be lowered if request and reply merge or raised if a frame splits. The sniffer never transmits. Send the files; one capture per protocol, with the inverter connected and running, is enough to write and test a decoder, the same way the CAN dialects were done. If the baud rate or format is not 9600 8N1, say what the BMS / inverter manual states (`-baud`, `-parity`, `-stop`).

### Probing a polled protocol without an inverter

`sniff` only sees traffic if something polls the BMS. With only the BMS connected, use `probe`: it sends read-only requests and prints the BMS's replies, in the same style as `sniff`.

```
lmh-can probe /dev/ttyUSB4 -device battery -style pylon -out pylon_probe.txt          # standard Pylontech get-commands, addresses 2 and 1
lmh-can probe /dev/ttyUSB4 -device battery -style pylon -adr 0,1,2,3 -baud 9600       # try other addresses
lmh-can probe /dev/ttyUSB4 -ascii '~20024642E00202FD33\r'             # one raw ASCII request
lmh-can probe /dev/ttyUSB4 -hex '01 03 00 00 00 0a c5 cd'             # one raw request in hex
lmh-can probe /dev/ttyUSB4 -modbus -slave 1 -start 0 -count 110       # dump Modbus holding registers (decimal, hex, signed)
```

The `pylon` set sends only "get" commands (protocol version, manufacturer, system parameters, analog values, alarms, charge/discharge management, serial number). Replies are checked (LENGTH and CHKSUM) and their INFO bytes printed. The BMS must be set to the Pylon RS485 protocol in PbmsTools. Request sets for other protocols can be added once their request format is known (vendor document or an inverter capture).

### Adding a CAN protocol

1. On the BMS, select the protocol in PbmsTools.
2. Capture a few minutes: `candump -L can1 > capture.log`.
3. Check what the existing decoders understand: `lmh-can replay -protocol pylontech -file capture.log`. IDs shown as `NOT decoded` need a decoder.
4. Get the vendor's frame specification. Implement the decoder in `internal/protocol/`, add the capture to the tests with expected values, register it, and mark it `Unverified` until the values match the BMS display (PbmsTools).

## Build

```
go test ./...
go build -o lmh-can ./cmd/lmh-can                                                    # native
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o lmh-can ./cmd/lmh-can   # for the LMH
```

## Configure the CAN interface

The daemon never changes interface settings. It checks that the interface is up and that the kernel bitrate matches the config, and refuses to start otherwise.

```
sudo ip link set can1 down
sudo ip link set can1 type can bitrate 500000 listen-only on
sudo ip link set can1 up
```

On this unit the back-panel CAN0 terminals appear as `can1`; confirm with `ip -br link | grep -i can`. `examples/lmh-can-iface@.service` does this at boot. If a BMS stays silent in listen-only mode, try `listen-only off`.

## First-time setup

```
./lmh-can setup
```

An interactive wizard. It asks device (BMS / inverter / generator) and link (CAN / RS485), then:

- **CAN:** protocol, bitrate, the CAN interface (it lists those it finds), a device name, listen-only, optional MQTT, and where to save the config. It shows the `ip link` commands and runs them with `sudo` if you agree.
- **RS485:** protocol, serial port (it lists `/dev/ttyACM*` and `/dev/ttyUSB*`), baud, data format (8N1 / 8E1 / 8O1 / 8N2), device address, a device name, optional MQTT, and where to save the config.

Then it writes the config file and offers to show live data (`listen` for CAN, `poll` for RS485). **BMS over CAN or RS485** is implemented; inverter and generator print what is missing and exit. CAN interface settings do not survive a reboot; use `examples/lmh-can-iface@.service` for that.

## Run

```
lmh-can listen can1                                     # quick CAN bench check: JSON lines on stdout, no config, no MQTT
lmh-can poll /dev/ttyACM1 -device battery -protocol pacemodbus -once   # quick RS485 bench check (see RS485)
lmh-can sniff /dev/ttyACM1 -out capture.txt             # record raw RS485 traffic
lmh-can run -config /etc/lmh-can/config.json            # publish to MQTT
lmh-can run -config examples/config.json -stdout        # print instead of publishing
lmh-can replay -protocol pylontech -file capture.log    # decode a candump -L capture
lmh-can protocols
```

### Config (`examples/config.json`)

| Key | Meaning |
|---|---|
| `mqtt.host`, `port`, `client_id`, `username`, `password` | Broker (port defaults to 1883) |
| `mqtt.topic_prefix` | Default `lmh` |
| `buses[].interface` | CAN interface name, e.g. `can1` |
| `buses[].bitrate` | Must match the kernel setting |
| `rs485[].port` | Serial port, e.g. `/dev/ttyACM1` or a `/dev/serial/by-path/...` name |
| `rs485[].baud`, `parity`, `stop_bits` | Default: the protocol's baud (9600), `N`, `1` |
| `rs485[].slave` | Device address 1-247, default 1 for pacemodbus |
| `rs485[].device` | Same keys as `buses[].device` below; see `examples/config-rs485.json` |
| `device.name` | Letters, digits, `-`, `_`; used in the topic |
| `device.class` | `battery` (the only one implemented) |
| `device.protocol` | See the table above |
| `device.topic` | Optional override of `<prefix>/<name>` |
| `device.interval_s` | Publish period, default 1 |
| `device.stale_after_s` | No decoded frame for this long = stale, default 10 |

One device per CAN interface (and per RS485 port) is enforced. For CAN: these protocols use fixed frame IDs with no sender address, so two devices on one bus would overwrite each other.

## MQTT

| Topic | Payload | Retained |
|---|---|---|
| `<prefix>/<name>/state` | JSON state, once voltage, current and SOC have all arrived | yes |
| `<prefix>/<name>/status` | `waiting`, `online` or `stale` | yes |
| `<prefix>/lmh-can/status` | `online` / `offline` (broker last-will) | yes |

Example state:

```json
{"device":"bms1","protocol":"pylontech","ts":1793290951,"stale":false,"bus_errors":0,
 "voltage_v":55.95,"current_a":-0.7,"power_w":-39.2,"soc_pct":99,"soh_pct":100,"temperature_c":33.2,
 "charge_voltage_limit_v":54.4,"charge_current_limit_a":0,"discharge_current_limit_a":200,"discharge_voltage_limit_v":47,
 "modules":1,"protection_raw":"0200","alarm_raw":"0000","request_flags_raw":"0000","id":"pN"}
```

- Fields a protocol does not report are omitted, not zero.
- `current_a` is positive while charging. `power_w` is computed.
- `protection_raw`, `alarm_raw` and `request_flags_raw` are hex as received; their bit meanings are not decoded.
- `stale: true` means no frame was decoded for `stale_after_s`; the values are the last ones seen.
- `bus_errors` counts kernel error frames since start (CAN) or failed polls (RS485).
- RS485 PACE Modbus adds `cell_temps_c`, `mosfet_temp_c`, `ambient_temp_c`, `cell_temp_min_c` / `cell_temp_max_c`, `flags` (`warnings`, `protections`, `status` by name; present with empty lists when nothing is set), the raw registers (`alarm_raw`, `protection_raw`, `status_raw`, `balance_mask_raw`), and `version`, `model_serial`, `serial`. It does not set `temperature_c`: no single pack temperature is defined, so use the cell temperature min / max. Unused cell slots are left out of `cells_mv`.
- Messages are QoS 0. The device status is re-published every 10 s and the service status on every reconnect.

## Layout

```
cmd/lmh-can        command line (setup, listen, poll, sniff, probe, run, replay, protocols)
internal/wizard    interactive setup
internal/canbus    SocketCAN, fake bus, candump parser, interface check
internal/serialport  termios serial port (stdlib), in-memory fake
internal/modbus    Modbus RTU master (read holding registers)
internal/rs485     RS485 protocol registry, PACE Modbus poller, Pylon ASCII framing, sniffer, probe requests
internal/protocol  decoders and the protocol registry
internal/model     normalized Battery state
internal/engine    per-device loop: decode, stale detection, publish
internal/publish   MQTT client, stdout and in-memory publishers
internal/config    JSON config loading and validation
examples/          config, systemd units, a real Pylontech capture
```

## Limitations

- Battery class only; inverter and generator classes are not implemented (an inverter's own RS485 / Modbus telemetry needs its register map, an inverter model and a generic device state in the engine).
- Classic CAN only (no CAN FD); standard 11-bit frame IDs for the current decoders.
- No transmit path in use on CAN, so polled CAN protocols (such as PACE's own CAN protocol) are not supported yet. `Bus.Send` exists for them.
- RS485: Modbus RTU function 3 only, one device per port, no register writes. pacemodbus, pylon and wow are decoded from the real BMS; the other five protocols need a capture or the vendor document first. Pylon-style protocols are ASCII frames, not Modbus, and have their own poller.
- The built-in MQTT client is publish-only, QoS 0, no TLS. Use a local broker, or swap it for a full client behind the `Publisher` interface if TLS is needed.
- Stale values are kept, not cleared; consumers must check `stale`.
