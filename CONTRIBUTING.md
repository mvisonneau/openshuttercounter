# Contributing

The most useful thing you can contribute is a reading from your own camera —
whether or not it worked.

Right now exactly one body has been confirmed by someone holding it. Every
other camera is either an educated guess or not supported at all, and the only
way that changes is people running this on their own gear and saying what
happened.

## Telling us about your camera

Run it:

```bash
openshuttercounter --trace mycamera.json
```

Then open an issue with the trace attached and say what happened. All three of
these outcomes are worth reporting:

- **It printed numbers that look right.** Confirm them with the two-trace
  method below and your camera goes straight into the supported list.
- **It printed numbers that look wrong.** Wildly off, all zeroes, stuck. The
  trace shows what the camera actually replied, which is usually enough to work
  out where the real counter lives.
- **It failed outright.** Not recognised, refused to open, errored. Still
  useful, and still a trace worth having.

A trace is a record of everything exchanged with the camera: the commands sent
and the replies received. It contains no photographs, no personal data and
nothing about the contents of your card. It does include the camera's USB
serial number, so remove that from the file if you would rather not publish it.

## Confirming a camera

A camera counts as confirmed when its counters have been shown to move by
exactly the number of frames taken. That needs two traces and about ten
minutes, and nothing besides the camera itself.

```bash
openshuttercounter --trace before.json

# take an exact number of frames — 20 is plenty

openshuttercounter --trace after.json --note "20 frames between traces"
```

Compare them:

```bash
openshuttercounter decode before.json --only 9052,9053
openshuttercounter decode after.json  --only 9052,9053
```

If every counter advanced by exactly the number of frames you took, that is a
confirmed camera. Open a pull request adding it to the model list for its
manufacturer — for Canon that is
[`pkg/manufacturers/canon/models.go`](pkg/manufacturers/canon/models.go) —
attaching both traces and saying how many frames you took:

```go
{
    Name:        "EOS 80D",
    ProductID:   0x****,
    RatedLife:   100000,
    Confirmed:   true,
    ConfirmedOn: "firmware 1.0.2",
    Counters: []Counter{
        {Label: "Shutter actuations", Address: 0x****, Length: 10},
    },
},
```

The USB product ID comes from `openshuttercounter list`.

If the numbers did **not** move by the frame count, they are not your body's
counters. Attach both traces to an issue anyway: the two together are exactly
what is needed to find the right ones, since the difference between them is the
only thing that identifies a counter.

## Adding a manufacturer

Only Canon is implemented so far. The rest of the project is deliberately
manufacturer agnostic, so adding another one means writing a single package and
naming it in one place:

1. Implement `manufacturers.Reader` in `pkg/manufacturers/<name>/`. It gets an
   open PTP session and the USB device it belongs to, and returns a
   `manufacturers.Reading`.
2. Add it to the registry in
   [`internal/cmd/utils.go`](internal/cmd/utils.go).

Everything else — claiming the USB interface, PTP framing, tracing, output,
JSON, the CLI — is already shared and needs no changes.

Cameras from any maker are worth a try before writing any code: the standard
PTP shutter counter property is tried first on every camera, and plenty of
bodies still answer it.

## Other things that would help

- **Confirming the Windows backend against real hardware.** It opens the still
  image device interface with `CreateFile` and exchanges containers with
  `ReadFile` / `WriteFile`, which follows from how Windows exposes PTP cameras
  but has not been run against a camera yet.
- **Explaining the varying low byte offset** in Canon's replies. The decoder
  takes the last non-zero byte after offset 1. That works on every reply seen
  so far, but it is an empirical rule rather than an understood format. A third
  trace at a different frame count, say 7, would probably settle it.
- **Identifying Canon's MPU `0x1015`.** Larger than both the shutter and mirror
  counts, advances at the same rate, purpose unknown.
- **Reaching macOS USB without libusb.** ImageCaptureCore's
  `requestSendPTPCommand:` would remove the only runtime dependency the project
  has, and would work with `ptpcamerad` rather than against it.
- **Upstreaming to [libgphoto2](https://github.com/gphoto/libgphoto2).**
  [`ptp_canon_eos_905f()`](https://github.com/gphoto/libgphoto2/blob/master/camlibs/ptp2/ptp.c)
  has been dead code since 2019 and is called from nowhere. A patch adding a
  factory command function and a `/main/status/shuttercounter` entry for
  DIGIC 6+ bodies would put this in every Linux distribution. The people
  waiting for it are in
  [gphoto2 #81](https://github.com/gphoto/gphoto2/issues/81),
  [libgphoto2 #270](https://github.com/gphoto/libgphoto2/issues/270) and
  [libgphoto2 #308](https://github.com/gphoto/libgphoto2/issues/308).

## What will not be merged

Anything that writes to the camera.

On Canon bodies, `FA_SetProperty`, `FA_SetCalendar`, `MonWrite` and `MonCall`
write factory state: calibration data, the calendar, and the actuation counters
themselves. `pkg/manufacturers/canon` refuses to build them, a test asserts the
refusal, and that is not up for negotiation. Shutter count fraud is the reason
readers like this one get distrusted, and a read-only tool is worth more than a
complete one.

The same goes for any equivalent on a manufacturer added later, and for blind
opcode sweeps: vendor opcode ranges hold write and call primitives, and these
bodies also advertise `UpdateFirmware`, `FormatStore` and `DeleteObject`.

## Development

```bash
make test        # tests, with the race detector
make lint-all    # lint every platform's USB backend, not only the host's
make build-all   # cross compile every supported target
make all         # all of the above
```

Two things are worth knowing before changing anything under
`pkg/manufacturers/`:

- **The framing tests are ground truth, not a formality.** They assert that
  what this code builds is byte-identical to what a camera has already been
  shown to accept. A malformed payload puts a Canon body into Err 70. If a
  framing test fails, the change is wrong: do not update the fixture to match
  the code, and do not run the result against a camera.
- **Only one platform's USB backend compiles on any given machine.** Run
  `make lint-all` and `make build-all` before opening a pull request, or CI
  will find what your laptop could not.
