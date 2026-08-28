# The Canon EOS factory command channel over PTP

Reference documentation for the vendor PTP interface that carries shutter and
mirror actuation counters on Canon DIGIC 6 and newer bodies.

Everything here was confirmed against an **EOS 5D Mark IV, firmware 1.2.1**.
Claims that were not directly observed are marked as such.

Confirming it on another body needs nothing but the camera: see
CONTRIBUTING.md.

---

## Background: why the usual methods fail

Up to roughly DIGIC 5, Canon exposed a shutter counter as PTP device property
`0xD1AC` (`PTP_DPC_CANON_EOS_ShutterCounter`). libgphoto2 reads it as
`/main/status/shuttercounter`, and it works on a 5D Mark III.

On DIGIC 6/6+ bodies it is gone. On the 5D Mark IV, `GetDevicePropValue` on
`0xD1AC` returns:

```
rc = 0x2005   (OperationNotSupported)
```

This is not a hidden or unadvertised property. It is removed.

Three further avenues were checked and found empty on this body:

| Avenue | Result |
|---|---|
| EOS event property table (`0x9114`/`0x9115`/`0x9116`) | 120 properties, ~11.7 KB. Diffed across 20 known actuations at every byte offset, widths 2/4/8, both endiannesses. **No value tracks actuations.** |
| Read-only status opcodes (`0x9108`, `0x913F`, `0x9135`, `0x9136`, `0x9149`, `0x9147`, `0x9145`, `0x914F`, `0x9123`, `0x9101`) | Only `0x9108 GetDeviceInfoEx` responds (612 bytes), **byte-identical** before and after 20 actuations. |
| Canon EDSDK | Documented API does not expose the counter on DIGIC 6+. |

### Approaches that were tested and failed

Each of these was tried empirically on this body. They are recorded so nobody
repeats them.

| Approach | Outcome |
|---|---|
| `GetDevicePropValue(0xD1AC)` | `rc=0x2005` OperationNotSupported. Genuinely removed. |
| EOS event property table (`0x9114`/`0x9115`/`0x9116`) | 120 properties, ~11.7 KB. Diffed across 20 actuations at every offset, widths 2/4/8, both endiannesses. Nothing tracks actuations. |
| 10 read-only status opcodes | Only `0x9108 GetDeviceInfoEx` responds (612 bytes), byte-identical before and after. |
| `0x905F` with args `0x0D` / `0x17` | `DeviceBusy` in every state tried: with and without remote mode, with and without event mode, after `SetUILock`, after five different `0x905x` opcodes. The 2018 forum capture's opcode was simply wrong. |
| `SetUILock (0x911B)` as a precondition | Accepted, changed nothing. |
| FreeShutterCounter, EOS Digital Info, webshutter, camerainspector | All stop before DIGIC 6+; all inherit libgphoto2's model list. |
| EOSCount | Uses the official EDSDK path, supports through DIGIC V only. |
| Canon EDSDK | The documented API does not expose the counter on DIGIC 6+. |

The reason all of these fail is architectural: **the counters are not stored on
the main DryOS/ICU processor at all.** They live in the memory of the MPU — a
separate Toshiba TX19A microcontroller that drives the shutter, mirror and
metering. The PTP property tree is a view onto the wrong processor.

Corroborating evidence: a firmware symbol dump for the EOS 40D, posted on the
[Magic Lantern forum][ml], lists `MonOpen`, `MonClose`, `MonRead`,
`MonReadAndGetData`, `MonWrite`, `MonCall`, `FA_TestMonRead`, `FA_TestMonCall`,
`FA_TestMonReadAndWrite` and `m_hSemaphoreDataComp`, all attributed to source
file `../MpuMonitor.c`.

---

## The channel

Two vendor opcodes carry a text-based command interface.

| Opcode | Direction | PTP parameters | Purpose |
|---|---|---|---|
| `0x9052` | data-OUT | `(0, 1)` if a reply follows, `(0, 0)` if not | send a command |
| `0x9053` | data-IN | `(0, 0, 1)` | read the reply |

Both **require a data phase**. Calling either with parameters alone returns
`0x2019 DeviceBusy`, which is what made this channel look like a dead end for
years — including to libgphoto2's maintainer, whose 2019 attempt on an EOS 750D
hit exactly this.

### `0x9052` payload layout

```
<command name>        NUL-terminated ASCII
<u32 argc>            number of argument records
<argc x arg record>   see below
<zero padding>        24 bytes when argc == 0, 4 bytes otherwise
```

Argument record, 20 bytes:

```
<u32 tag>             2 for an integer, or a four character ASCII code
<u32 value>
<12 bytes zero>
```

The first word is a **type tag**, not a constant. `MonReadAndGetData` passes
the integer `2`, but `FA_GetProperty` tags its records with the ASCII string
`"CSC"` (`43 53 43 00`) and puts a structured value in the second word. Both
encodings have been observed on the wire, and `openshuttercounter decode`
renders them as `MonReadAndGetData(0x2, 0x100b, 0xa)` and
`FA_GetProperty(CSC=0x2000001, CSC=0x0)` respectively.

The final argument record in a multi-argument command is followed by 16 bytes
of zero rather than 12, giving the trailing 4 bytes noted above.

Both encodings are reproduced by `Command.MarshalBinary` in
`pkg/manufacturers/canon`, whose tests assert the result byte for byte against
payloads a camera has been observed to accept. The framing described here is
therefore checked on every test run rather than transcribed once and trusted,
which matters because a malformed payload is what produces Err 70.

### Command vocabulary

`FA_` denotes **Factory**, and `Mon` the MPU monitor.

| Command | Status |
|---|---|
| `MonOpen`, `MonClose`, `MonReadAndGetData` | Observed on the wire, and what this project sends |
| `FA_GetProperty` | Observed on the wire; returns a struct beginning with the firmware version |
| `MonRead`, `MonWrite`, `MonCall`, `FA_TestMonRead`, `FA_TestMonCall`, `FA_TestMonReadAndWrite` | Listed in the EOS 40D firmware symbol dump against `../MpuMonitor.c` [(Magic Lantern forum)][ml] |
| `FA_SetProperty`, `FA_SetCalendar` | Known to exist on this channel; they write factory state and are refused outright, see Safety below |

The list is certainly incomplete. A body that answers something not named here
is worth reporting.

---

## Reading the counters

### Session

An observed session, in order:

1. `SetDevicePropValue (0x1016)` on property `0xD406` with the UTF-16LE string
   `"CameraShutterCount"`. Property `0xD406` appears in `gphoto2 --summary` as
   *Unknown Initiator*; this is the host identifying itself. **Not required for
   the read** — omitting it works.
2. `FA_GetProperty` with a `"CSC"`-tagged argument. Returns a struct beginning
   with the firmware version as ASCII (`"1.2.1"`), then `"96(3a)"`, then several
   integers. Useful as a liveness check, not needed for the counters.
3. `MonOpen`, argc 0, no reply.
4. `MonReadAndGetData` — one per address.
5. `MonClose`, argc 0, no reply.

### `MonReadAndGetData`

Three arguments: `(2, address, length)`.

```
MonReadAndGetData\0
03 00 00 00                    argc = 3
02 00 00 00  02 00 00 00  <12 zero>     arg 1: constant 2
02 00 00 00  0b 10 00 00  <12 zero>     arg 2: MPU address 0x100B
02 00 00 00  0a 00 00 00  <16 zero>     arg 3: length 10
```

### MPU address map

Established by taking exactly 20 frames between two captures:

| Address | Length | Contents | Before | After |
|---|---|---|---|---|
| `0x1001` | 10 | Shutter actuations | 26,354 | 26,374 |
| `0x100B` | 10 | Mirror actuations | 26,128 | 26,148 |
| `0x1015` | 10 | Larger total counter | 28,034 | 28,054 |
| `0x1198` | 32 | Error log — **not** a counter | unchanged | unchanged |

The shutter/mirror assignment follows from mirror count sitting below shutter
count, since live-view frames actuate the shutter without moving the mirror.
The exact meaning of `0x1015` is not established.

### Reply encoding

Each 10-byte reply encodes its value as:

```
value = (byte[1] << 8) | <low byte>
```

The low byte's offset within the reply **varies** — observed at 7, 8 and 9.
Taking the last non-zero byte after offset 1 works on all observed data, but
this is an empirical workaround, not an understood format.

The encoding is confirmed by a carry event. At `0x1001` the low byte read 242
before the 20 frames; 242 + 20 = 262, which overflows a byte. In the second
capture the high byte at offset 1 had incremented from `0x66` to `0x67` and the
low byte read 6. A coincidence would not carry correctly.

```
0x1001  before: 00 66 00 00 00 00 00 00 f2 00     (0x66 << 8) | 242 = 26354
0x1001  after:  00 67 00 00 00 00 00 00 00 06     (0x67 << 8) |   6 = 26374
```

---

## Safety

**Malformed payloads to `0x9052` cause Err 70 on the camera.** This was
confirmed the hard way, by sending bare command names without the `argc` field,
argument records or PTP parameters. The error cleared on a battery pull with no
lasting damage, but do not improvise the framing — capture a known-good tool
and match its bytes.

`FA_SetProperty`, `FA_SetCalendar`, `MonWrite` and `MonCall` write factory
state: calibration data, the counters themselves, the calendar. Commercial
service software advertises the ability to rewrite the shutter counter and the
whole MPU EEPROM through this same layer.

`pkg/manufacturers/canon` refuses to build any of them —
`Command.MarshalBinary` returns an error rather than bytes — and a test asserts
the refusal. That is deliberate
and is not an oversight to be tidied up: a read-only tool is worth more than a
complete one here.

Blind opcode sweeps across `0x9050`–`0x905F` are a bad idea. That range
contains write and call primitives, and the body also advertises
`0x911F UpdateFirmware`, `0x9106 FormatStore` and `0x9105 DeleteObject`.

---

## Portability

Everything above is from **one body at one firmware version**. The MPU address
map in particular should not be assumed to hold elsewhere. Extending it to
another body is the same experiment that produced it: read the camera, take an
exact number of frames, read it again, and keep whatever advanced by exactly
that number. `openshuttercounter --trace` and `openshuttercounter decode` exist
for precisely this, and CONTRIBUTING.md walks through it.

The channel itself is transport independent. Only the plumbing underneath
differs, and this repository implements three of them, none of which needs the
camera's driver replaced:

| Platform | Backend | cgo | Driver swap | Run against a camera |
|---|---|---|---|---|
| Linux | usbfs ioctls on `/dev/bus/usb` | no | no | yes |
| macOS | libusb-1.0, loaded at runtime via purego | no | no | not yet |
| Windows | still image device interface, `CreateFile` + `ReadFile` / `WriteFile` | no | no | not yet |

### The Windows transport

Windows exposes every PTP camera through the still image device interface class
`{6BDD1FC6-810F-11D0-BEC7-08002BE2092F}`, backed by `usbscan.sys`. An
application enumerates it with SetupAPI, opens the resulting device path with
`CreateFile`, and exchanges PTP containers with `WriteFile` and `ReadFile`. The
I/O is pipe style rather than `DeviceIoControl` based.

The useful consequence is that a Windows implementation needs neither libusb
nor a WinUSB driver swap, so the manufacturer's own software keeps working
alongside it. That is what this project's Windows backend does.

**It has not been run against a camera yet.** Confirming it takes one person
with a Windows machine and a camera:

```
openshuttercounter --trace windows.json
```

### The macOS transport

libusb is loaded at runtime with purego rather than linked with cgo, which
keeps `CGO_ENABLED=0` across the project and lets every target cross compile
from one machine. IOKit's USB device interfaces are CoreFoundation plug-ins and
are considerably more work to drive through a runtime FFI than a flat C API,
which is why libusb is there at all.

Going through ImageCaptureCore's `requestSendPTPCommand:` instead would remove
the dependency altogether and would cooperate with `ptpcamerad` rather than
displacing it. That is the right long-term answer and is not done yet.

## Prior work

- [**There must be a way to read shutter actuations from Digi6/6+**][thread],
  SourceForge `canon-eos-digital-info`, opened by **Garfield** on 2017-04-14.
  Where anyone researching this ends up.
- **DD4DA**, same thread, 2018-05-24 — the original USB analysis, and the first
  public mention of `MonOpen` / `MonReadAndGetData` / `MonClose`, with the
  observation that the procedure appears nowhere in Canon's SDK. What was
  posted showed only data-in phases, which is how the opcode came to be
  recorded as `0x905F`.
- **Marcus Meissner**, same thread, 2019-02-03 — hypothesised `0x905F` with a
  single argument selecting the counter (`0x17` for mirror, `0x0d` for
  shutter), and reported `DeviceBusy` on an EOS 750D. He
  [committed `ptp_canon_eos_905f()`][commit] to libgphoto2 the same day; it is
  [still in the tree][ptpc] and has never been called from anywhere.
- [**Magic Lantern forum topic 1452**][ml], the Canon 40D thread, carrying the
  1.1.1 firmware symbol dump that lists `MonOpen`, `MonClose`, `MonRead`,
  `MonReadAndGetData`, `MonWrite` and `MonCall` against `../MpuMonitor.c`, and
  identifies the MPU as a Toshiba TX19A.
- Open and unresolved: [gphoto2 #81][i81] (EOS 5DS),
  [libgphoto2 #270][i270] (EOS 750D), [libgphoto2 #308][i308] (EOS 6D Mark II).

[thread]: https://sourceforge.net/p/canon-eos-digital-info/discussion/general/thread/d58f1905/
[commit]: https://github.com/gphoto/libgphoto2/commit/39266fc2173e36b7e80345923928e91f4f7c4eb1
[ptpc]: https://github.com/gphoto/libgphoto2/blob/master/camlibs/ptp2/ptp.c
[ml]: https://www.magiclantern.fm/forum/index.php?topic=1452.0
[i81]: https://github.com/gphoto/gphoto2/issues/81
[i270]: https://github.com/gphoto/libgphoto2/issues/270
[i308]: https://github.com/gphoto/libgphoto2/issues/308

The corrections this repository offers:

- The opcodes are `0x9052` / `0x9053`, not `0x905F`.
- The interface is a text command channel, not a counter selector.
- The missing precondition was simply the data phase. Both opcodes require one,
  and without it either returns `DeviceBusy` — which is what every previous
  attempt ran into.
- An argument record's first word is a type tag rather than a constant. It is
  the integer 2 for `MonReadAndGetData` and the ASCII code `"CSC"` for
  `FA_GetProperty`.
