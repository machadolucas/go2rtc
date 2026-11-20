// eldenc - AAC-ELD encoder for HomeKit talkback, using macOS AudioToolbox.
// stdin:  PCM s16le mono at the given sample rate (default 16000)
// stdout: frames, each prefixed with a 2 byte big endian length
// HomeKit cameras use AAC-ELD with 480 samples per frame, which ffmpeg can't produce.
#include <AudioToolbox/AudioToolbox.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define FRAME 480

typedef struct { int16_t buf[FRAME]; int eof; } Input;

static ssize_t read_full(int fd, void *p, size_t n) {
    size_t got = 0;
    while (got < n) {
        ssize_t r = read(fd, (char *)p + got, n - got);
        if (r <= 0) return got;
        got += r;
    }
    return got;
}

static OSStatus input_proc(AudioConverterRef c, UInt32 *nPackets, AudioBufferList *data,
                           AudioStreamPacketDescription **desc, void *user) {
    Input *in = user;
    if (in->eof) { *nPackets = 0; return noErr; }
    ssize_t n = read_full(0, in->buf, sizeof(in->buf));
    if (n < (ssize_t)sizeof(in->buf)) {
        in->eof = 1;
        if (n <= 0) { *nPackets = 0; return noErr; }
        memset((char *)in->buf + n, 0, sizeof(in->buf) - n);
    }
    data->mNumberBuffers = 1;
    data->mBuffers[0].mData = in->buf;
    data->mBuffers[0].mDataByteSize = sizeof(in->buf);
    data->mBuffers[0].mNumberChannels = 1;
    *nPackets = FRAME;
    return noErr;
}

int main(int argc, char **argv) {
    double rate = argc > 1 ? atof(argv[1]) : 16000;
    UInt32 bitrate = argc > 2 ? atoi(argv[2]) : 24000;

    AudioStreamBasicDescription in = {0}, out = {0};
    in.mSampleRate = rate;
    in.mFormatID = kAudioFormatLinearPCM;
    in.mFormatFlags = kLinearPCMFormatFlagIsSignedInteger | kLinearPCMFormatFlagIsPacked;
    in.mBitsPerChannel = 16; in.mChannelsPerFrame = 1;
    in.mBytesPerFrame = 2; in.mFramesPerPacket = 1; in.mBytesPerPacket = 2;

    out.mSampleRate = rate;
    out.mFormatID = kAudioFormatMPEG4AAC_ELD;
    out.mChannelsPerFrame = 1;
    out.mFramesPerPacket = FRAME;

    AudioConverterRef conv;
    OSStatus err = AudioConverterNew(&in, &out, &conv);
    if (err) { fprintf(stderr, "eldenc: AudioConverterNew %d\n", (int)err); return 1; }

    UInt32 mode = kAudioCodecBitRateControlMode_Constant;
    AudioConverterSetProperty(conv, kAudioCodecPropertyBitRateControlMode, sizeof(mode), &mode);
    err = AudioConverterSetProperty(conv, kAudioConverterEncodeBitRate, sizeof(bitrate), &bitrate);
    if (err) fprintf(stderr, "eldenc: bitrate %d\n", (int)err);

    UInt32 size = sizeof(out);
    AudioConverterGetProperty(conv, kAudioConverterCurrentOutputStreamDescription, &size, &out);
    UInt32 maxPacket = 0; size = sizeof(maxPacket);
    AudioConverterGetProperty(conv, kAudioConverterPropertyMaximumOutputPacketSize, &size, &maxPacket);
    if (maxPacket == 0 || maxPacket > 8192) maxPacket = 8192;

    UInt32 cookieSize = 0;
    AudioConverterGetPropertyInfo(conv, kAudioConverterCompressionMagicCookie, &cookieSize, NULL);
    fprintf(stderr, "eldenc: rate=%.0f frames_per_packet=%u max_packet=%u cookie=%u\n",
            out.mSampleRate, (unsigned)out.mFramesPerPacket, (unsigned)maxPacket, (unsigned)cookieSize);
    if (out.mFramesPerPacket != FRAME) { fprintf(stderr, "eldenc: encoder refused 480 samples per frame\n"); return 2; }

    Input input = {0};
    unsigned char *buf = malloc(maxPacket);
    for (;;) {
        AudioBufferList list;
        list.mNumberBuffers = 1;
        list.mBuffers[0].mNumberChannels = 1;
        list.mBuffers[0].mDataByteSize = maxPacket;
        list.mBuffers[0].mData = buf;
        UInt32 n = 1;
        AudioStreamPacketDescription desc;
        err = AudioConverterFillComplexBuffer(conv, input_proc, &input, &n, &list, &desc);
        if (n == 0) break;
        UInt32 len = list.mBuffers[0].mDataByteSize;
        unsigned char hdr[2] = { len >> 8, len & 0xff };
        if (write(1, hdr, 2) != 2 || write(1, buf, len) != (ssize_t)len) break;
        if (err) break;
    }
    return 0;
}
