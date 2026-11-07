# lmh-can

Battery and inverter monitor for the LMH edge device (Seeed reComputer Industrial R21xx). It reads a BMS over CAN or RS485, and an inverter over RS485, decodes it into a typed, normalized state, and publishes JSON to MQTT.

```
BESS / inverter  ->  LMH (lmh-can)  ->  MQTT  ->  backend  ->  PostgreSQL  ->  EMS
```

- **CAN is receive only**: the driver never transmits on the bus.
- **RS485 is request / response**: the LMH is the master and polls. It sends Modbus (or protocol-specific) *read* requests only; it never writes a register. `sniff` is receive only.
- Standard library only, including a small built-in MQTT client. One static binary; cross-compiles for `linux/arm64`.
- Two device kinds, kept completely separate: **battery** (CAN or RS485) and **inverter** (RS485 only). Each has its own protocol list. Generators are not implemented.

## Contents

1. [Status](#status)
2. [Protocols](#protocols)
3. [RS485: wiring and bench tools](#rs485-wiring-and-bench-tools)
4. [RS485 battery protocols](#rs485-battery-protocols)
5. [RS485 inverter protocols](#rs485-inverter-protocols)
6. [Capturing an unsupported protocol](#capturing-an-unsupported-protocol)
7. [CAN](#can)
8. [First-time setup and running](#first-time-setup-and-running)
9. [Config](#config)
10. [MQTT](#mqtt)
11. [Layout](#layout)
12. [Limitations](#limitations)

## Status

Every protocol carries one of four levels. `lmh-can protocols` prints them.

| Level | Meaning |
|---|---|
| **verified** | Compared live against the vendor tool or an independent reader |
| **captured** | Decoder written from, and tested against, replies of the real device in that mode; values cross-checked with the device's other readings. No live comparison with an inverter / vendor tool of that brand |
| **unverified** | Implemented from a vendor document; never run against a real device |
| **unsupported** | No specification or capture yet. Selecting it is an error; no layout is guessed |

**Tested:** `go vet` and `go test -race ./...` pass. The tests include the real captures (CAN frames of a LYTVOLT S15000 in every CAN mode it offers, and RS485 replies from the same BMS in the Pylon, WOW, Voltronic and Luxpower modes). The RS485 battery protocols were run on the real BMS through an FT232 USB-RS485 adapter on the LMH (`/dev/ttyUSB4`). The serial code and the `poll` / `run` paths are also exercised on a pseudo-terminal against fake devices.

**Not tested on hardware yet:** the SocketCAN reader on the LMH, the CAN interface check (parses `ip -details -json`), the systemd units, the built-in MQTT client against a real broker, `run` with an `rs485` section, and the whole inverter class (SRNE is written from the vendor document and has only talked to a fake inverter). Run `lmh-can run -stdout` on the LMH first.

## Protocols

### CAN (battery)

Names are the BMS's own CAN protocol names; `jin_lang`, `senergyinv` and `tbb_lithium` are accepted as aliases.

| Level | Protocols |
|---|---|
| verified (compared live with PbmsTools) | pylontech |
| captured | goodwe, growatt, studer, srd, victron, victron23, luxpower, schneider, sma, sofar, pv, jinlang, senergy, tbb, megarevo |
| unsupported | deye, gigacity, afore, sorotec, must, solis |

"Captured" CAN dialects had no vendor specification: frame meanings were worked out from the capture and cross-checked against the battery (all 14 agree on voltage, current, SOC, temperature and limits; Growatt and Schneider cycle counts match the RS485 reading; Schneider's pack serial equals the RS485 serial). Fields that could not be identified are left undecoded; alarm and protection bits are reported raw.

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

Notes from the captures: the old tool's Studer frames (0xB0/0xC0/0xC1) do not appear, this BMS sends Goodwe-style frames in Studer mode; some modes report no discharge voltage limit (goodwe, studer) or no SOH (srd), and those fields are omitted, not zero; Victron-style temperatures (0x373) are whole kelvin; several unsupported names may be RS485-only on this BMS.

### RS485 battery

| Level | Protocols |
|---|---|
| verified | pacemodbus |
| captured | pylon, wow, voltronic, luxpower_vo3 |
| unsupported | growatt, se, pylon_f |

### RS485 inverter

| Level | Protocols |
|---|---|
| unverified | srne |

## RS485: wiring and bench tools

On the LMH tested, **RS485-3 (A3 / B3 / GND3) is `/dev/ttyACM1`**; an FT232 USB adapter showed up as `/dev/ttyUSB4`. Device names can change between boots: for a permanent setup use a stable name from `ls -l /dev/serial/by-path/` as the `port` in the config.

- The on-board ports are isolated: connect **GND** as well as A and B (a missing GND corrupted frames).
- No reply: swap A and B first. A/B labels are not consistent between vendors.
- The on-board ports do not need (and do not support) the Linux RS485 ioctl; direction is handled in hardware.
- The BMS must be set to the matching protocol in PbmsTools for the RS485 port you connect to. The BMS has one RS485 protocol per mode.
- RS485 is polled: a BMS does not broadcast, so `sniff` only sees traffic if something (an inverter) polls it. With only the BMS connected, use `probe`.

### The `-device` flag

Batteries and inverters are never mixed up and the kind is never guessed, neither from the device nor from the protocol name. On the command line it is the required **`-device battery|inverter`** flag of `poll` (and of `probe -style`); in the config it is the required `class` field. A protocol is looked up only in that device's list, so the same name could exist in both lists without clashing. A wrong pairing is refused with a hint:

```
$ lmh-can poll /dev/ttyUSB4 -device battery -protocol srne
error: "srne" is an inverter protocol, not a battery one: use -device inverter
```

### Commands

```
lmh-can poll  PORT -device battery  -protocol pacemodbus [-baud 9600] [-parity N] [-stop 1] [-slave 1] [-once] [-interval 1] [-stale 10]
lmh-can poll  PORT -device inverter -protocol srne       [same flags]
lmh-can probe PORT -device battery  -style pylon|voltronic|luxpower [-slave N] [-adr 2,1] [-out file]
lmh-can probe PORT -device inverter -style srne                     [-slave N] [-out file]
lmh-can probe PORT -ascii '~20024642E00202FD33\r'        one raw ASCII request
lmh-can probe PORT -hex '01 03 00 00 00 0a c5 cd'        one raw request, hex
lmh-can probe PORT -modbus -slave 1 -start 0 -count 110  dump Modbus holding registers (decimal, hex, signed)
lmh-can sniff PORT -baud 9600 [-seconds 120] [-gap 15] [-out capture.txt]    receive only
```

- `poll -once` reads once and prints indented JSON; without it, one JSON line per `-interval`. `-slave` defaults to the protocol's address.
- `probe` sends read-only requests and prints each reply (TX / RX as hex plus ASCII). The raw modes (`-ascii`, `-hex`, `-modbus`) need no `-device`.
- `-modbus` dumps with decimal addresses: a table's register N is address N-1.

## RS485 battery protocols

### pacemodbus (verified)

PACE BMS Modbus RTU V1.3, function 3, slave 1, 9600 8N1. Registers 0-39 every poll (current, voltage, SOC / SOH, capacities, cycles, up to 16 cell voltages, temperatures, limits, warning / protection / status flags decoded by name) and the version / serial strings once. The Go port reads the same values as the earlier Python reader on the real BMS.

### pylon (captured)

Pylontech RS485 protocol: ASCII frames `~ VER ADR CID1 CID2 LENGTH INFO CHKSUM \r`, VER 20, CID1 46. Written from `probe` replies of this BMS, not from the vendor document. Per poll: 0x42 (analog values: 16 cell voltages, 5 temperatures, current, voltage, remaining and total capacity, cycles) and 0x92 (charge / discharge limits, flags byte); manufacturer (0x51) and serial (0x93) once.

- The BMS answers **address 2 only** (`-slave` is the ADR byte, default 2).
- No SOC field: `soc_pct` is derived as remaining / total capacity, so it is not an integer like the BMS display.
- `temps_c` lists the five sensors in protocol order; which is cell / MOSFET / ambient is not identified, so none is mapped to `temperature_c`.
- Current is signed 0.1 A, positive = charging. The flags byte of 0x92 is reported raw as `request_flags_raw`.
- The serial from 0x93 is cut at 16 characters.
- Alarm info (0x44) and system parameters (0x47) got no reply, so alarms / protections are not available in this mode.

### wow (captured)

Modbus RTU, slave 1, 9600 8N1. A register dump of the BMS in WOW mode shows registers 0-39 are the **same block as the PACE map with the PACE scales** (current 0.01 A, cells in mV, limits 0.1 V / 0.1 A), so the decoder reuses it. Cross-checked against Pylon replies from the same BMS (cells add up to the pack voltage, 279.91 Ah, 50 cycles, 54.4 V charge limit, 0 A charge current limit, 200 A discharge limit).

- The vendor's "WOW Comprehensive Modbus Map" PDF is **wrong for this BMS**: it gives current 0.1 A and cells 0.01 V, and its "limit" registers hold protection thresholds in other units. Do not decode from the PDF.
- Addresses 60-109 look like the protection parameter table; not decoded.
- Flag registers 9 / 10 / 11 are reported raw (`alarm_raw`, `protection_raw`, `status_raw`); their bit meanings in this mode are not confirmed. Version / serial are not read.

### voltronic (captured)

From Voltronic's "Inverter and BMS 485 communication Protocol" (2020), checked on the BMS (voltage, capacities and limits match the Pylon and PACE readings). The frames look like Modbus RTU but the reply carries a 2-byte **word** count where Modbus has a 1-byte byte count, so it has its own client. Slave 1 is the common address.

- Reads `0x10-0x35` (cells, temperatures, current, voltage, SOC, total capacity), `0x36-0x3F` (packs online, alarm / protect words, design capacity), `0x70-0x77` (limits, enable bits, remaining capacity); version once.
- Cell voltages are really 0.1 V here (33 = 3.3 V), so cells read 3300 mV flat and **no min / max / spread is published**. The scale is still checked against the module voltage in case another firmware uses mV.
- Current = charge current minus discharge current. This BMS has 4 temperature sensors (the document says 6) and unused slots read 0xFFFF; they appear as `temps_c`, none mapped to the MOSFET.
- `charge_disabled` in the status flags is the BMS asking the charger to stop (its charge current limit is 0 A).
- Alarm / protect words are decoded with the document's bit names and also published raw; seen at zero only so far.

### luxpower_vo3 (captured)

From "Communication Protocol between Luxpowertek Hybrid inverter and battery, RS485" V0.3. Modbus function 3, 9600 8N1, slave 1, 16 registers from address 0, but **little-endian**: the request is `01 03 00 00 10 00` plus CRC, and each data word in the reply is low byte first. Checked on the BMS: it answers this request and rejects the standard big-endian one with exception 3.

- Registers: charge voltage, max charge / discharge current, SOC (low byte) / SOH (high byte), full capacity, voltage, current, status bits, fault code, warning code, max / min cell mV, max / min cell temperature, parallel number, cycles.
- Full capacity is whole Ah only (279 for 279.91). Cell min / max / spread are real mV here (3351-3358 mV in the capture).
- Fault and warning codes are published raw (the document gives no bit meanings); seen at zero only.

### growatt, se, pylon_f (unsupported)

No document and no capture of a request format.
- **pylon_f**: `probe -device battery -style pylon` got no reply to any of the 14 requests (addresses 2 and 1, 9600 8N1), so it is not plain Pylon at those settings.
- **growatt**: needs the vendor's "SPF BMS RS485 protocol" document, or a `probe -modbus` dump.
- **se**: probably Schneider; needs a dump or document.

## RS485 inverter protocols

Inverters publish the same `<topic>/state` and `<topic>/status` pair as batteries, but with an inverter record: battery side, PV inputs, charging, grid, output, load, temperatures, faults and energy counters. A field the protocol does not report is omitted, not zero.

### srne (unverified)

From "Energy storage inverter MODBUS communication protocol V1.7" (2022-07-04), also used by the HF / HFP series (checked against the HFP4850S80-H manual). Standard Modbus RTU, function 3, 9600 8N1, at most 32 registers per read, register addresses in hex, 32-bit values low word first. **Not run on a real inverter yet.**

What is read:

| Block | Content | When |
|---|---|---|
| `0x100`-`0x111` | battery SOC / voltage / current / temperature, PV 1 and 2, charge state and power | every poll |
| `0x200`-`0x21F` | fault bits and codes, machine state, bus, grid, output, load | every poll |
| `0x220`-`0x225` | heat sink temperatures, PV charge current | every poll, best effort |
| `0xF02D`-`0xF03F`, `0xF046`-`0xF04B` | today's and accumulated energy counters, working hours | every poll, best effort; a block the inverter rejects is not asked again |
| `0x14`-`0x17`, `0x1A`-`0x1D`, `0x35`- | versions, RS485 address, model code, protocol version, serial | once |

- A full cycle takes about one to two seconds (Modbus gap between requests).
- The second PV input is published only after it has reported a non-zero value.
- Fault codes are named from the HFP manual's table (section 6.2); codes not in it appear as numbers. `fault_bits_raw` is the raw 64-bit field.
- Battery current is positive while charging. Temperatures: controller (high byte of `0x103`) and battery (low byte) in whole degrees; a sensor reading 0 is not reported.
- Not decoded: settings (`0xE000` and up), the 7-day history, fault history, phase B / C values. The driver never writes.

**Wiring and settings (HFP series manual, section 5.2):**
- RJ45 RS485 port: **pin 7 = A, pin 8 = B, pin 2 = GND**. Pin 1 is a +5 V supply: do **not** connect it to the LMH.
- RS485-2 is the PC / telecom port; RS485-1 is meant for the WiFi / GPRS module.
- In the inverter's setup menu, set RS485-2 communication to **[32] SLA** (default), not **[32] BMS** (in BMS mode the port expects a battery, not a master).
- The inverter's RS485 address is menu parameter 14 (default 1); pass another one with `-slave`.
- The manual also lists a USB-B port (CH340 USB-serial) for the vendor's PC software. Plugging that into the LMH should appear as a `/dev/ttyUSB*` port; whether it speaks the same register map has **not** been checked. It avoids the RS485 wiring and the [32] setting.

**First test:**

```
lmh-can probe /dev/ttyUSB4 -device inverter -style srne -out srne_probe.txt
lmh-can poll  /dev/ttyUSB4 -device inverter -protocol srne -once
```

If nothing answers: swap A and B; try the address shown by parameter 14 (`-slave`); check the RS485-2 mode is SLA; check GND. Send the probe output if it still fails.

## Capturing an unsupported protocol

For a protocol with no document: set it on the BMS in PbmsTools, connect the LMH **in parallel** with the inverter on the RS485 lines (A to A, B to B, GND), keep the stub short, and record while the inverter is polling:

```
lmh-can sniff /dev/ttyACM1 -baud 9600 -seconds 120 -out capture.txt
```

Each line is one burst of bytes (normally a request or a reply) with time, length, hex and ASCII. `-gap` (ms of silence that ends a burst, default 15) can be lowered if request and reply merge or raised if a frame splits. The sniffer never transmits. One capture per protocol, with the inverter connected and running, is enough to write and test a decoder. If the line is not 9600 8N1, say what the manual states (`-baud`, `-parity`, `-stop`).

Without an inverter, `probe` can only ask questions the tool already knows how to phrase (the styles above, or a raw request). A BMS that only answers its own inverter's exact query needs the inverter capture.

### Adding a CAN protocol

1. On the BMS, select the protocol in PbmsTools.
2. Capture a few minutes: `candump -L can1 > capture.log`.
3. `lmh-can replay -protocol pylontech -file capture.log`: IDs shown as `NOT decoded` need a decoder.
4. Get the vendor's frame specification, implement the decoder in `internal/protocol/`, add the capture to the tests with expected values, register it, and mark it `Unverified` until the values match the BMS display.

## CAN

The daemon never changes interface settings. It checks that the interface is up and that the kernel bitrate matches the config, and refuses to start otherwise.

```
sudo ip link set can1 down
sudo ip link set can1 type can bitrate 500000 listen-only on
sudo ip link set can1 up
```

On this unit the back-panel CAN0 terminals appear as `can1`; confirm with `ip -br link | grep -i can`. `examples/lmh-can-iface@.service` applies the settings at boot. If a BMS stays silent in listen-only mode, try `listen-only off`. `lmh-can listen can1 -protocol pylontech` is the quick CAN bench check. CAN is battery only.

## First-time setup and running

### Build

```
go test ./...
go build -o lmh-can ./cmd/lmh-can                                                          # native
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o lmh-can ./cmd/lmh-can   # for the LMH
```

### Wizard

```
./lmh-can setup
```

It asks what you are connecting (**BMS / inverter** / generator), then:

- **BMS:** the link (CAN or RS485).
  - *CAN:* protocol, bitrate, interface (it lists those it finds), device name, listen-only, optional MQTT, config path. It shows the `ip link` commands and runs them with `sudo` if you agree.
  - *RS485:* protocol, serial port (it lists `/dev/ttyACM*` and `/dev/ttyUSB*`), baud, data format (8N1 / 8E1 / 8O1 / 8N2), device address, name, optional MQTT, config path.
- **Inverter:** RS485 only, same questions as RS485 above, with the inverter protocol list.
- **Generator:** not implemented; it says so and exits.

It writes the config (with the right `class`) and offers to show live data (`listen` for CAN, `poll -device ...` for RS485).

### Run

```
lmh-can run -config /etc/lmh-can/config.json         # publish to MQTT
lmh-can run -config examples/config.json -stdout     # print instead of publishing
lmh-can protocols                                    # list protocols and their levels
lmh-can version
```

`examples/lmh-can.service` runs it under systemd (with `SupplementaryGroups=dialout` for the serial ports).

## Config

Examples: `examples/config.json` (CAN), `examples/config-rs485.json` (RS485 battery), `examples/config-inverter.json` (RS485 inverter).

| Key | Meaning |
|---|---|
| `mqtt.host`, `port`, `client_id`, `username`, `password` | Broker (port defaults to 1883) |
| `mqtt.topic_prefix` | Default `lmh` |
| `buses[].interface` | CAN interface name, e.g. `can1` |
| `buses[].bitrate` | Must match the kernel setting |
| `rs485[].port` | Serial port, e.g. `/dev/ttyACM1` or a `/dev/serial/by-path/...` name |
| `rs485[].baud`, `parity`, `stop_bits` | Default: the protocol's baud (9600), `N`, `1` |
| `rs485[].slave` | Device address 1-247, default: the protocol's (1; 2 for pylon) |
| `device.name` | Letters, digits, `-`, `_`; used in the topic |
| `device.class` | **Required**: `battery` (CAN or RS485) or `inverter` (RS485 only). No default |
| `device.protocol` | From the list of that class |
| `device.topic` | Optional override of `<prefix>/<name>` |
| `device.interval_s` | Poll / publish period, default 1 |
| `device.stale_after_s` | No good reading for this long = stale, default 10 |

One device per CAN interface and per RS485 port is enforced (CAN protocols use fixed frame IDs with no sender address, so two devices on one bus would overwrite each other). Device names must be unique across both link types. A config without `class` is rejected with a message saying so.

## MQTT

| Topic | Payload | Retained |
|---|---|---|
| `<prefix>/<name>/state` | JSON state, once the core values have arrived (battery: voltage, current, SOC; inverter: battery voltage and machine state) | yes |
| `<prefix>/<name>/status` | `waiting`, `online` or `stale` | yes |
| `<prefix>/lmh-can/status` | `online` / `offline` (broker last-will) | yes |

Common rules:
- Fields a protocol does not report are omitted, not zero.
- `stale: true` means no good reading for `stale_after_s`; the values are the last ones seen (consumers must check `stale`). `bus_errors` counts kernel error frames (CAN) or failed polls (RS485) since start.
- Messages are QoS 0. The device status is re-published every 10 s and the service status on every reconnect.

### Battery state

```json
{"device":"bms1","protocol":"pylontech","ts":1793290951,"stale":false,"bus_errors":0,
 "voltage_v":55.95,"current_a":-0.7,"power_w":-39.2,"soc_pct":99,"soh_pct":100,"temperature_c":33.2,
 "charge_voltage_limit_v":54.4,"charge_current_limit_a":0,"discharge_current_limit_a":200,"discharge_voltage_limit_v":47,
 "modules":1,"protection_raw":"0200","alarm_raw":"0000","request_flags_raw":"0000","id":"pN"}
```

- `current_a` is positive while charging; `power_w` is computed.
- `protection_raw`, `alarm_raw`, `request_flags_raw` are hex as received. Bit meanings are decoded to names only where a protocol documents them (`flags.warnings` / `protections` / `status`, present with empty lists when nothing is set; so far PACE Modbus and Voltronic; Luxpower reports status flags only). `flags` entries with `null` lists mean "not decoded", not "none".
- RS485 batteries can add `cell_temps_c`, `temps_c` (sensors in protocol order, role unknown), `mosfet_temp_c`, `ambient_temp_c`, `cell_temp_min_c` / `cell_temp_max_c`, `status_raw`, `balance_mask_raw`, `version`, `model_serial`, `serial`, `manufacturer`. `temperature_c` is set only where one pack temperature is defined. Unused cell slots are left out of `cells_mv`.

### Inverter state

Example with synthetic values (`class` is always `"inverter"`):

```json
{"device":"inv1","class":"inverter","protocol":"srne","ts":1793972198,"stale":false,"bus_errors":0,
 "state":"mains powered operation","state_code":4,
 "battery_soc_pct":90,"battery_voltage_v":51.2,"battery_current_a":67,"battery_power_w":3430.4,"battery_temp_c":25,
 "charge_state":"constant voltage","charge_power_w":5000,"pv_charge_current_a":12.3,"mains_charge_current_a":0,
 "pv":[{"voltage_v":350,"current_a":12,"power_w":4200}],"pv_power_w":4200,
 "grid_voltage_v":230.1,"grid_current_a":5.5,"grid_frequency_hz":50.01,
 "output_voltage_v":230,"output_current_a":6.2,"output_frequency_hz":50,"bus_voltage_v":390,
 "load_current_a":7,"load_power_w":1500,"load_apparent_va":1600,"load_pf":0,"load_pct":30,
 "temps_c":{"controller":41,"heatsink_dcac":48,"heatsink_dcdc":45.2},
 "fault_codes":[14],"faults":["inverter overload"],"fault_bits_raw":"0000000000000000",
 "today":{"pv_energy_kwh":15.6,"battery_charge_ah":40},"totals":{"pv_energy_kwh":1200.5},
 "version":"software 1.05 / 1.06, hardware 2.00 / 2.01","model_code":7,"rs485_address":1,"running_days":200}
```

- `battery_current_a` is positive while charging. `battery_power_w` and `pv_power_w` (sum of the inputs) are computed.
- `today` and `totals` hold the energy counters the inverter keeps; only counters the inverter reported appear.
- `faults` are names for the codes the protocol documents; unknown codes appear as `fault <n>`.

## Layout

```
cmd/lmh-can           command line (setup, listen, poll, sniff, probe, run, replay, protocols)
internal/wizard       interactive setup (battery and inverter)
internal/canbus       SocketCAN, fake bus, candump parser, interface check
internal/serialport   termios serial port (stdlib), in-memory fake
internal/modbus       Modbus RTU master (read holding registers)
internal/rs485        RS485 protocol registries (batteries, inverters), pollers, Pylon ASCII framing,
                      Voltronic and Luxpower clients, SRNE driver, sniffer, probe requests
internal/protocol     CAN decoders and the CAN protocol registry
internal/model        normalized Battery and Inverter state
internal/engine       per-device loop (CAN frames or polled Source): decode, stale detection, publish
internal/publish      MQTT client, stdout and in-memory publishers
internal/config       JSON config loading and validation
examples/             configs, systemd units, a real Pylontech capture
```

## Limitations

- **Device kinds:** battery (CAN, RS485) and inverter (RS485 only). Generators are not implemented. Inverters over CAN are not implemented.
- **Inverter:** SRNE is the only inverter protocol and has not run on a real inverter. Telemetry only, no settings.
- **CAN:** classic CAN only (no CAN FD); 11-bit frame IDs for the current decoders; no transmit path in use, so polled CAN protocols (PACE's own CAN protocol) are not supported yet (`Bus.Send` exists for them).
- **RS485:** read requests only, one device per port, no register writes. Modbus-style protocols differ in framing (Voltronic: word-count length; Luxpower: little-endian), so each has its own small client. Pylon-style protocols are ASCII frames with their own poller. Three battery protocols (growatt, se, pylon_f) are unsupported.
- **MQTT:** the built-in client is publish-only, QoS 0, no TLS. Use a local broker, or swap in a full client behind the `Publisher` interface if TLS is needed.
- **Stale values are kept, not cleared.**
- **Bit meanings** for alarm / protection / status words are decoded only where a document or a capture confirmed them; otherwise they are raw hex.
