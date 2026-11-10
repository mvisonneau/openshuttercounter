# Sony Alpha: the shutter count in the image maker note

Reference for how openshuttercounter reads Sony Alpha bodies.

Everything here was observed on an **Alpha 7 IV (ILCE-7M4), firmware 6.02**,
an **Alpha 7C II (ILCE-7CM2), firmware 2.0**, and an **Alpha 1 II (ILCE-1M2),
firmware 4.0**. Claims that were not directly observed are marked as such.

---

## Where the count is not

| Avenue | Result |
|---|---|
| Standard PTP device properties | In MTP mode the body advertises **no device properties at all**. |
| Sony Camera Remote SDK (v1.08) | No property or command for an actuation count. The nearest name, `CrDeviceProperty_Movie_IntervalRec_CountDownIntervalTime`, is a movie timer. |
| Remote Shooting mode | The body answers PTP with Sony's vendor extension (`0x92xx` operations, `0xC2xx` events), but `GetStorageIDs` returns `0x2013 StoreNotAvailable`: the card is not offered. |

Whether one of the Remote Shooting vendor properties carries a count has not
been checked. Reading them needs Sony's SDIO connect handshake, which changes
the camera's state, and the maker note route below needs none of that.

## Where it is

Every image a Sony body writes carries the count. It is in the EXIF maker
note, in tag `0x9050`, enciphered.

```
TIFF header (JPEG APP1 "Exif\0\0", or the start of an ARW)
  IFD0 -> 0x8769 ExifIFD
    -> 0x927C MakerNote: "SONY DSC \0\0\0" then an IFD, offsets from the TIFF header
      -> 0x9050  undefined[N], enciphered
```

### The cipher

A byte `b` below 249 is stored as `b³ mod 249`. Bytes 249 to 255 are stored as
they are. Because 249 = 3 × 83 and 3 is coprime to both 2 and 82, cubing is a
bijection on 0..248, so the cipher inverts exactly with a lookup table. This
is the scheme ExifTool documents in `Image::ExifTool::Sony::Decipher`.

### The layouts

The deciphered block's layout differs by generation. ExifTool names them
`Tag9050`, `Tag9050a`, `b`, `c` and `d`. openshuttercounter decodes `b`, `c`
and `d`, and picks the layout from the model the camera reports. A body not in
its model list is refused rather than decoded with a guessed layout.

**`Tag9050b`**: ILCE-6100, 6300, 6400, 6500, 6600, 7C, 7M3, 7RM2, 7RM3, 7RM4,
7SM2, 9, 9M2, ILCA-99M2. Used from mid 2015 to 2020. ExifTool places the
shutter field and the count at **the same offsets as `Tag9050c`**, below, and
applies the count to every one of these bodies without conditions. **None has
been tried.** They are listed because the two fields sit where they were read
on the Alpha 7 IV, and the rest of the path, PTP to EXIF to cipher, is shared
with the three bodies that have been read.

**`Tag9050c`**: ILCE-1, 7M4, 7RM5, 7SM3, FX3.

| Offset | Format | Contents |
|---|---|---|
| `0x26` | int16u[3] | Shutter. All zero for a frame taken with the electronic shutter. |
| `0x3A` | int32u, low 24 bits | **Shutter count** |

**`Tag9050d`**: ILCE-6700, 7CM2, 7CR, FX2.

| Offset | Format | Contents |
|---|---|---|
| `0x0A` | int32u, low 24 bits | **Mechanical shutter count**. ExifTool notes it does not go up for electronic shutter frames. |

**`Tag9050d`, mechanical frames only**: ILCE-1M2, 7M5. These bodies write the
`Tag9050d` layout only for frames taken with the mechanical shutter, which
ExifTool recognises by **five leading zero bytes** in the block. Electronic
shutter frames use a layout nobody has decoded, and carry no count either
tool can find. The reader checks the prefix, skips frames without it, and
tries up to eight images, newest first. If none has the prefix, it says so and
asks for one frame with the mechanical shutter.

### Left out on purpose

| Bodies | Why |
|---|---|
| `Tag9050a`: Alpha 7, 7R, 7S, 7 II, 5000, 5100, 6000, NEX and SLT | ExifTool says the count at `0x32` only *seems* valid, and that it wraps at 65,536 on the Alpha 7R. A tool that reports a wrapped number as a shutter count is worse than one that refuses. |
| ZV-E10, ZV-E10 II, ZV-E1, FX30 | No mechanical shutter. Where the block has a count at all, it counts images, not actuations. |
| Compact `DSC-` bodies | ExifTool decodes no `0x9050` block for them. |

Any of these is a good candidate for a trace: two traces an exact number of
frames apart would show whether the offset holds.

### How much a listing promises

`openshuttercounter models` shows one of three states for each body:

| State | Meaning |
|---|---|
| confirmed | Somebody holding it took a known number of frames and the count moved by exactly that many. |
| read on a camera, not yet confirmed | Read over USB, and the value matched ExifTool on the same image. |
| untried | Inferred from ExifTool's layout tables alone. |

### Observed

| Body | Image | Read | ExifTool 13.55, same bytes |
|---|---|---|---|
| Alpha 7 IV, fw 6.02 | `DSC02395.ARW`, mechanical | 38,298 | 38,298 |
| Alpha 7C II, fw 2.0 | `DSC05458.ARW` | 5,450 | 5,450 |
| Alpha 1 II, fw 4.0 | `DSC01869.ARW` to `DSC01876.ARW`, electronic | no count | no count |
| Alpha 1 II, fw 4.0 | `DSC01901.ARW`, mechanical | 474 | 474 |

**The Alpha 1 II is confirmed.** 25 frames taken in a row with the mechanical
shutter, `DSC01877.ARW` to `DSC01901.ARW`, read 450, 451, … 474: up by exactly
one per frame, with no gaps. Every one of them starts with the five zero
bytes. The electronic frames before them all start `0c 71 df 3d` or
`0c 71 df 3f` instead.

The owner believed they had used only the electronic shutter, but the first
mechanical frame already read 450. Where the earlier 449 came from is **not
known**. Factory testing is one possibility. Another is the body's option to
close the shutter at power off, if those closings are counted; that has not
been tested.

The Alpha 7 IV and Alpha 7C II have not been confirmed by the frame count method yet. Whether the
Alpha 7 IV count goes up for electronic shutter frames is **not yet known**.
ExifTool describes the `Tag9050c` field as the total number of exposures, and
notes that on an ILCE-7RM2 it does not go up in silent shooting.

## The session

All read only, standard PTP operations:

1. `OpenSession`
2. `GetDeviceInfo`. Must list `GetObjectHandles` and `GetObjectInfo`.
3. `GetStorageIDs`
4. `GetObjectHandles(storage, 0, 0)`: every object on the card.
5. `GetObjectInfo` from the last handle backwards, up to 32 images
   (`.JPG`, `.ARW`, `.HIF`). The newest by capture date is used. The Alpha 7
   IV lists objects oldest first, so the newest is the last handle. One
   `GetObjectInfo` takes about 50 ms, so scanning a full card would be slow.
6. `GetPartialObject(handle, 0, 512 KiB)`, else `GetObject` for the whole file.
7. `CloseSession`

## USB modes

| Mode | USB ID | Works |
|---|---|---|
| Image Transfer (MTP) | Alpha 7 IV `054c:0da6`, Alpha 7C II `054c:0e8b`, Alpha 1 II `054c:0eeb` | **Yes.** Plain PTP, 24 operations, vendor extension ID `0xFFFFFFFF`. |
| Remote Shooting | Alpha 7 IV `054c:0da7` | No. PTP with Sony's extension, but the card is not offered. |
| Image Transfer (MSC) | — | No. USB mass storage, no PTP at all. |
| Live Streaming | — | No. A USB video device. |

## Limits

- The count is the count **when the newest image on the card was taken**. Take
  one frame just before reading if the number must be current. The output
  names the file and its capture time so this is never hidden.
- An empty card, or a card with only movies, gives no reading.
- Older generations (`Tag9050`, `a`, `b`) store the count at other offsets,
  so their bodies, the Alpha 7 III among them, are not in the model list yet.
- On `Tag9050d` bodies the output cannot say which shutter the newest image
  used, because the block does not record it.
- On the Alpha 1 II and 7 V, a reading is the count as of the newest
  **mechanical** frame among the eight newest images, which may be older than
  the newest image. The output names the file it came from.
