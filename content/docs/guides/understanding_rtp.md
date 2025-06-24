---
title: Understanding RTP
weight: 20
---


## Understanding RTP

Here we may go deep dive for some RTP concepts

### RTP Timestamp 

RTP timestamp follows sampling clock rate which is not the same as following Real Time duration.

Every audio stream will have constant RTP Timestamp increase based on samples generated, but in case 
of stopping current stream and starting new, this RTP timestamp difference must be calculated. 

Value should not be based whether streamer is slow or fast in pushing RTP packets

### RTP Timestamp when pause or no audio present
So in case you have pauses in audio stream, RTP timestamp still need to continue and recalculated based on Real Time.
If RTP timestamp is not recalculated it will affect RTCP metrics as well and therefore wrong calc of jitter and etc.

In case of non Real Time audio, like streaming pre downloaded audio, sampling must still apply.
This means many packets may be sent at once, but RTP timestamp will be virtually increased.

#### RTP Timestamp modifications visual

1. Stream Write: `Timestamp = 0`; increase += 160 (20ms Sample duration)

2. Stream Stop: `Timestamp = 1600`; Written = 10 Frames/Samples

3. Pause: `100ms` = 5 * 160 = `800`

4. Stream Write: `Timestamp = 1600 + 800 = 2400`;  increase += 160 (20ms Sample duration)