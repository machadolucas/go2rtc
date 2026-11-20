# eldenc

AAC-ELD encoder for the two-way audio of HomeKit cameras (`homekit_client.talk_encoder`
in [FORK.md](../../FORK.md)). macOS only: it uses the AudioToolbox encoder, which
can produce the 480-samples-per-frame variant HomeKit needs.

- stdin: PCM s16le mono, 16000 Hz
- stdout: AAC-ELD frames, each prefixed with a 2-byte big-endian length

```bash
clang -O2 -o eldenc eldenc.c -framework AudioToolbox -framework CoreFoundation
./eldenc 16000 24000 < in.pcm > out.eld    # sample rate, bitrate
```
