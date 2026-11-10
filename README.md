# openshuttercounter

Read the shutter actuation count of a camera, on Linux, macOS and Windows, for
free.

```
$ openshuttercounter

  Canon EOS 5D Mark IV  (USB 04a9:3281)

  Shutter actuations           26,374   (MPU 0x1001)
  Mirror actuations            26,148   (MPU 0x100b)
  Total counter                28,054   (MPU 0x1015)

  Rated life 150,000 actuations - 17.6% used
```

One static binary. No libgphoto2, no vendor SDK, no Windows-only tooling.

## Why this exists

I wanted the shutter count on my own 5D Mark IV. It ought to be a solved
problem — the camera knows the number, the cable is right there — but every
open source tool I tried came back empty, and the ones that worked were paid,
closed and Windows only. For a number the camera will happily tell you, that
seemed a poor state of affairs, so I went looking for why.

It turns out the free tools are not at fault. Canon moved the counter, and
nobody had published the way to the new place. So I worked it out and wrote it
down, which is really what this repository is: the answer, in enough detail
that the next person does not have to go looking.

Up to roughly the DIGIC 5 generation, Canon exposed a shutter counter as PTP
device property `0xD1AC`. `gphoto2` reads it as `/main/status/shuttercounter`
and it still works on a 5D Mark III.

On DIGIC 6 and newer bodies — 5D Mark IV, 5DS, 5DS R, 1D X Mark II, 80D and
everything after — that property is gone. `GetDevicePropValue` on `0xD1AC`
returns `OperationNotSupported`. This is why gphoto2, FreeShutterCounter, EOS
Digital Info and every other free tool comes up empty on a modern body: not an
oversight on their part, but a wall Canon put there.

The counters were never in the PTP property tree. **They live in the memory of
the MPU**, the separate microcontroller that drives the shutter and mirror, and
they are reachable through a text command channel Canon carries over two vendor
PTP opcodes. That channel, its framing and the MPU address map are documented
in full in [docs/PROTOCOL.md](docs/PROTOCOL.md).

## Status

Canon and Sony are implemented. One Canon body and one Sony body have been
confirmed by somebody holding them, and two more Sony bodies have been read and
checked against ExifTool.

| | State |
|---|---|
| Canon protocol and framing | Confirmed on an EOS 5D Mark IV, firmware 1.2.1 |
| Canon MPU address map | Confirmed on the same body, over an exact count of 20 frames |
| Canon bodies that answer the standard PTP property | Should work; tried first on every Canon camera |
| Other Canon bodies | Very likely the same channel, but the addresses are unconfirmed |
| Sony maker note decoding | Read on an Alpha 7 IV (fw 6.02) and an Alpha 7C II (fw 2.0); both match ExifTool on the same file |
| Sony Alpha 1, 7R V, 7S III, FX3 | Same layout as the Alpha 7 IV according to ExifTool, untried |
| Sony Alpha 7 III, 7R II/III/IV, 7S II, 7C, 9, 9 II, 6100–6600, 99 II | Count at the same offset as the Alpha 7 IV according to ExifTool, untried |
| Sony Alpha 7CR, 6700, FX2 | Same layout as the Alpha 7C II according to ExifTool, untried |
| Sony Alpha 1 II | **Confirmed** on firmware 4.0, over 25 mechanical frames. Count only in mechanical shutter frames |
| Sony Alpha 7 V | Same rule as the Alpha 1 II according to ExifTool, untried |
| Other Sony bodies | Refused: the count is elsewhere, uncertain, or absent. See [docs/SONY.md](docs/SONY.md) |
| Other manufacturers | Not implemented yet |
| Linux backend (usbfs) | Implemented |
| macOS backend (libusb via purego) | Run against a Sony Alpha 7 IV |
| Windows backend (still image device) | Implemented, not yet run against a camera |

**If your camera is not on the confirmed list, try it anyway.** It costs
nothing, it cannot write to the camera, and whether it works or not the result
is worth reporting:

```bash
openshuttercounter --trace mycamera.json
```

That produces a file recording everything exchanged with the camera, which is
enough for anyone else to confirm the reading or work out what went wrong.
[CONTRIBUTING.md](CONTRIBUTING.md) explains what to do with it. This is the
single most useful contribution to the project.

## Install

```bash
# macOS, via Homebrew
brew install mvisonneau/tap/openshuttercounter

# Windows, via Scoop
scoop bucket add https://github.com/mvisonneau/scoops
scoop install openshuttercounter

# Anywhere with a Go toolchain
go install github.com/mvisonneau/openshuttercounter/cmd/openshuttercounter@latest
```

Binaries, `.deb`, `.rpm` and `.apk` packages are attached to every
[release](https://github.com/mvisonneau/openshuttercounter/releases).

## Usage

```bash
openshuttercounter                          # read the connected camera
openshuttercounter --raw                    # also show the raw replies
openshuttercounter --json                   # machine readable output
openshuttercounter --trace session.json     # record the whole conversation
openshuttercounter list                     # what is connected
openshuttercounter models                   # cameras this build knows
openshuttercounter decode session.json      # print a recorded conversation
```

`decode` reads traces written by openshuttercounter, and USB captures in pcap
or pcapng format for anyone who already has one. Comparing two traces taken an
exact number of frames apart is how a camera gets confirmed.

## Setting up the camera

Whatever the platform:

- Built-in Wi-Fi **fully disabled**. On many bodies enabling it disables the
  USB port entirely, and the camera simply will not appear.
- A card in the camera.
- A data cable, plugged in directly rather than through a hub.
- USB 2 ports proved noticeably more reliable than USB 3 during this work.

### Sony Alpha

- Set **USB Connection Mode** to **Image Transfer (MTP)**, or choose it when
  the camera asks on connect. *Image Transfer (MSC)* makes the card a plain
  disk with no PTP, and *Remote Shooting* does not offer the card.
- Take one photo just before reading. Sony bodies record the count in each
  image, so the reading is the count when the newest image was taken.

### Linux

The `.deb` and `.rpm` packages install a udev rule that lets you read a camera
without root, for Canon and Sony cameras. Installing another way, copy it
from this repository yourself:

```bash
sudo cp packaging/udev/99-openshuttercounter.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules && sudo udevadm trigger
```

Then replug the camera. If you get *device is claimed by another process*, that
is gvfs, which grabs cameras on sight:

```bash
gio mount -u gphoto2://
systemctl --user mask gvfs-gphoto2-volume-monitor
```

### macOS

USB access goes through libusb, loaded at runtime:

```bash
brew install libusb
```

macOS hands every camera to `ptpcamerad` as soon as it appears.
openshuttercounter stops it and retries automatically when the device is busy;
launchd restarts it afterwards.

### Windows

No driver replacement is needed: the tool opens the still image device
interface the stock driver already exposes, so the manufacturer's own software
keeps working. Close anything else that might have the camera open.

## Safety

**This tool only reads.**

On Sony bodies it sends only standard PTP operations that read the card:
`GetStorageIDs`, `GetObjectHandles`, `GetObjectInfo` and `GetPartialObject`.

On Canon bodies it sends `MonOpen`, `MonReadAndGetData` and `MonClose`, and
nothing else. That restraint is not incidental: the same channel carries
`FA_SetProperty`, `FA_SetCalendar` and `MonWrite`, which write factory state —
calibration data, the camera's calendar, and the actuation counters themselves.
This code
[refuses to build those commands at all](pkg/manufacturers/canon/factory.go),
and the refusal is covered by a test.

Malformed payloads to Canon's `0x9052` put the camera into **Err 70**. That
happened twice while the channel was being worked out, and cleared on a battery
pull with no lasting damage, but it is the reason the command framing here is
asserted byte-for-byte rather than written from a description. If those tests
ever fail, do not run the result against a camera.

Do not sweep opcodes blind in the `0x9050`–`0x905F` range. It contains write
and call primitives, and these bodies also advertise `UpdateFirmware`,
`FormatStore` and `DeleteObject`.

## How it works

Canon cameras that still answer the standard PTP shutter counter property are
read that way. Canon bodies that do not are read over the factory command
channel:

```
0x9052   send a command   params (0,1) if a reply follows, else (0,0)
         data-OUT: <name NUL> <u32 argc> <argc x 20-byte argument record>
0x9053   read the reply   params (0,0,1)
```

Both opcodes **require a data phase**. Calling either with parameters alone
returns `DeviceBusy` — which is exactly what made this channel look closed for
seven years, including to libgphoto2's maintainer.

`MonReadAndGetData(2, address, length)` reads MPU memory:

| MPU address | Contents | Confirmed reading |
|---|---|---|
| `0x1001` | Shutter actuations | 26,354 → 26,374 after 20 frames |
| `0x100B` | Mirror actuations | 26,128 → 26,148 |
| `0x1015` | Larger total counter | 28,034 → 28,054 |
| `0x1198` | Error log, not a counter | unchanged |

Full details, including the reply encoding and the evidence for it, are in
[docs/PROTOCOL.md](docs/PROTOCOL.md).

Sony bodies have no counter property, and Sony's SDK has no call for one. They
write the count into every image instead, in an enciphered maker note block
(EXIF tag `0x9050`). openshuttercounter reads the head of the newest image on
the card over PTP and decodes it there. The format is in
[docs/SONY.md](docs/SONY.md).

## Repository layout

Everything except `pkg/manufacturers/<name>` is manufacturer agnostic, so
supporting a second maker means adding a package rather than rearranging the
rest.

| Path | Contents |
|---|---|
| `cmd/`, `internal/` | The CLI |
| `pkg/manufacturers` | What a camera maker has to provide to be readable |
| `pkg/manufacturers/canon` | Canon's factory channel, counter decoding and model list |
| `pkg/manufacturers/sony` | Sony's maker note decoding and model list |
| `pkg/ptp` | PTP over USB, enough of it to send vendor opcodes |
| `pkg/usb` | The three USB backends and the descriptor parsing they share |
| `pkg/trace` | Recording and reading back a conversation with a camera |
| `pkg/pcap` | Reading USB captures, for traces that came from elsewhere |
| `docs/` | The protocol references |

## Credit

The trail starts on a SourceForge thread with a title that says it all —
[*There must be a way to read shutter actuations from Digi6/6+*][thread] —
opened by **Garfield** in April 2017.

**DD4DA** answered it in May 2018 with the first public mention of `MonOpen`,
`MonReadAndGetData` and `MonClose`, noting that the procedure appears nowhere
in Canon's SDK. **Marcus Meissner**, libgphoto2's maintainer, followed up in
February 2019 with the first hypothesis about the opcode — *"It might be PTP
Command 0x905f with 1 argument, like 0x17 for mirror count and 0x0d for shutter
count?"* — reported `DeviceBusy` on an EOS 750D, and [committed a wrapper for
it][commit] the same day. `ptp_canon_eos_905f()` is [still in the tree][ptpc],
and has never been wired to anything.

The **Magic Lantern** community's [Canon 40D thread][ml] carries the firmware
symbol dump listing the `Mon*` family against `../MpuMonitor.c`, which is what
established that these are MPU monitor functions rather than anything to do
with the main processor.

Still open, and what this is ultimately for: [gphoto2 #81][i81] (EOS 5DS),
[libgphoto2 #270][i270] (EOS 750D) and [libgphoto2 #308][i308] (EOS 6D Mark II).

[thread]: https://sourceforge.net/p/canon-eos-digital-info/discussion/general/thread/d58f1905/
[commit]: https://github.com/gphoto/libgphoto2/commit/39266fc2173e36b7e80345923928e91f4f7c4eb1
[ptpc]: https://github.com/gphoto/libgphoto2/blob/master/camlibs/ptp2/ptp.c
[ml]: https://www.magiclantern.fm/forum/index.php?topic=1452.0
[i81]: https://github.com/gphoto/gphoto2/issues/81
[i270]: https://github.com/gphoto/libgphoto2/issues/270
[i308]: https://github.com/gphoto/libgphoto2/issues/308

What this project adds: the opcodes are `0x9052` / `0x9053`, not `0x905F`; the
interface is a text command channel rather than a counter selector; the missing
precondition was simply the data phase; and an argument record's first word is
a type tag, which may be an ASCII code rather than the integer 2.

## Licence

MIT. See [LICENSE](LICENSE).

MIT rather than the Apache-2.0 used across the rest of these repositories,
deliberately: the natural home for this work is
[libgphoto2](https://github.com/gphoto/libgphoto2), which is LGPL-2.1, and
Apache-2.0 cannot be merged into it. MIT can.
