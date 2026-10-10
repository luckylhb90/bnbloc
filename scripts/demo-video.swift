// Speeds up long static stretches of a screen recording and exports a
// README-sized H.264 MP4 plus a poster frame. Usage:
//   swift scripts/demo-video.swift <in.mp4> docs/assets/demo.mp4 docs/assets/demo-poster.png <posterSecond|-1>
// BITRATE (bits/s, default 1600000) tunes the size; GitHub READMEs load
// best under ~10 MB (the committed demo uses BITRATE=1100000). Pick a
// poster second that shows the result, or pass -1 for 80% through.
import AVFoundation
import AppKit
import CoreImage

let args = CommandLine.arguments
let inURL = URL(fileURLWithPath: args[1]), outURL = URL(fileURLWithPath: args[2]), posterURL = URL(fileURLWithPath: args[3])
let posterAt = Double(args[4]) ?? -1
let asset = AVURLAsset(url: inURL)
let duration = CMTimeGetSeconds(asset.duration)
let vtrack = asset.tracks(withMediaType: .video).first!

// 1. Sample 4 frames/s as 48x30 grayscale and find static stretches.
let gen = AVAssetImageGenerator(asset: asset)
gen.maximumSize = CGSize(width: 96, height: 60)
gen.requestedTimeToleranceBefore = .zero; gen.requestedTimeToleranceAfter = .zero
func pixels(_ t: Double) -> [UInt8]? {
  guard let cg = try? gen.copyCGImage(at: CMTime(seconds: t, preferredTimescale: 600), actualTime: nil) else { return nil }
  let w = 48, h = 30
  var buf = [UInt8](repeating: 0, count: w * h)
  let ctx = CGContext(data: &buf, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w, space: CGColorSpaceCreateDeviceGray(), bitmapInfo: 0)!
  ctx.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))
  return buf
}
let step = 0.25
var times: [Double] = []; var t = 0.0
while t < duration { times.append(t); t += step }
var frames = times.map { pixels($0) }
var still = [Bool](repeating: false, count: times.count)
for i in 1..<times.count {
  guard let a = frames[i - 1], let b = frames[i] else { continue }
  var diff = 0
  for k in 0..<a.count { diff += abs(Int(a[k]) - Int(b[k])) }
  still[i] = Double(diff) / Double(a.count) < 0.6   // mean abs diff per pixel (0–255)
}
frames.removeAll()
// Static runs of at least 3 s, keeping 0.75 s at real speed on each side.
var ranges: [(Double, Double)] = []
var i = 0
while i < still.count {
  if still[i] {
    var j = i; while j < still.count && still[j] { j += 1 }
    let s = times[i] + 0.75, e = (j < times.count ? times[j] : duration) - 0.75
    if e - s >= 1.5 { ranges.append((s, e)) }
    i = j
  } else { i += 1 }
}
let speed = 8.0
var saved = 0.0
for (s, e) in ranges { saved += (e - s) * (1 - 1 / speed); print(String(format: "static %.1f–%.1fs (%.1fs) → x%.0f", s, e, e - s, speed)) }

// 2. Compose: copy the video, then compress each static range (last first).
let comp = AVMutableComposition()
let ct = comp.addMutableTrack(withMediaType: .video, preferredTrackID: kCMPersistentTrackID_Invalid)!
try! ct.insertTimeRange(CMTimeRange(start: .zero, duration: asset.duration), of: vtrack, at: .zero)
ct.preferredTransform = vtrack.preferredTransform
for (s, e) in ranges.reversed() {
  let r = CMTimeRange(start: CMTime(seconds: s, preferredTimescale: 600), end: CMTime(seconds: e, preferredTimescale: 600))
  ct.scaleTimeRange(r, toDuration: CMTime(seconds: (e - s) / speed, preferredTimescale: 600))
}
print(String(format: "duration %.1fs → %.1fs", duration, CMTimeGetSeconds(comp.duration)))

// 3. Re-encode H.264 at a README-friendly bitrate, at most 1920 px wide.
try? FileManager.default.removeItem(at: outURL)
let natural = vtrack.naturalSize.applying(vtrack.preferredTransform)
let srcW = abs(natural.width), srcH = abs(natural.height)
let scale = min(1.0, 1920.0 / srcW)
let outW = Int((srcW * scale / 2).rounded()) * 2, outH = Int((srcH * scale / 2).rounded()) * 2
let reader = try! AVAssetReader(asset: comp)
let vcomp = AVMutableVideoComposition(propertiesOf: comp)
vcomp.renderSize = CGSize(width: outW, height: outH)
let instr = AVMutableVideoCompositionInstruction()
instr.timeRange = CMTimeRange(start: .zero, duration: comp.duration)
let layer = AVMutableVideoCompositionLayerInstruction(assetTrack: ct)
layer.setTransform(ct.preferredTransform.concatenating(CGAffineTransform(scaleX: scale, y: scale)), at: .zero)
instr.layerInstructions = [layer]
vcomp.instructions = [instr]
let rout = AVAssetReaderVideoCompositionOutput(videoTracks: [ct], videoSettings: [kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA])
rout.videoComposition = vcomp
reader.add(rout)
let writer = try! AVAssetWriter(outputURL: outURL, fileType: .mp4)
writer.shouldOptimizeForNetworkUse = true
let bitrate = Int(ProcessInfo.processInfo.environment["BITRATE"] ?? "1600000")!
let win = AVAssetWriterInput(mediaType: .video, outputSettings: [
  AVVideoCodecKey: AVVideoCodecType.h264, AVVideoWidthKey: outW, AVVideoHeightKey: outH,
  AVVideoCompressionPropertiesKey: [AVVideoAverageBitRateKey: bitrate, AVVideoMaxKeyFrameIntervalKey: 120,
                                    AVVideoProfileLevelKey: AVVideoProfileLevelH264HighAutoLevel],
])
win.expectsMediaDataInRealTime = false
writer.add(win)
reader.startReading(); writer.startWriting(); writer.startSession(atSourceTime: .zero)
let q = DispatchQueue(label: "enc"); let done = DispatchSemaphore(value: 0)
win.requestMediaDataWhenReady(on: q) {
  while win.isReadyForMoreMediaData {
    if let sb = rout.copyNextSampleBuffer() { win.append(sb) } else { win.markAsFinished(); writer.finishWriting { done.signal() }; return }
  }
}
done.wait()
guard writer.status == .completed else { print("export failed: \(String(describing: writer.error))"); exit(1) }
let size = (try? FileManager.default.attributesOfItem(atPath: outURL.path)[.size] as? Int) ?? 0
print(String(format: "exported %dx%d %.1f MB (%.1f Mbps)", outW, outH, Double(size) / 1_048_576, Double(bitrate) / 1e6))

// 4. Poster frame from the output (default: 80% through).
let out = AVURLAsset(url: outURL)
let pg = AVAssetImageGenerator(asset: out)
pg.maximumSize = CGSize(width: 1760, height: 1760)
pg.requestedTimeToleranceBefore = .zero; pg.requestedTimeToleranceAfter = .zero
let pt = posterAt >= 0 ? posterAt : CMTimeGetSeconds(out.duration) * 0.8
if let cg = try? pg.copyCGImage(at: CMTime(seconds: pt, preferredTimescale: 600), actualTime: nil) {
  // JPEG for .jpg/.jpeg (a fraction of a PNG's size for a screen photo), else PNG.
  let jpeg = ["jpg", "jpeg"].contains(posterURL.pathExtension.lowercased())
  let data = NSBitmapImageRep(cgImage: cg).representation(using: jpeg ? .jpeg : .png, properties: jpeg ? [.compressionFactor: 0.85] : [:])!
  try! data.write(to: posterURL)
  print(String(format: "poster at %.1fs", pt))
}
