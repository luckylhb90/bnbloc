// Renders the DMG window background from its HTML source.
//
//   swift apps/desktop/scripts/render-dmg-background.swift
//
// Reads bundle.macOS.dmg (windowSize, appPosition, applicationFolderPosition)
// from src-tauri/tauri.conf.json, injects them into src-tauri/dmg/background.html
// as CSS variables, and snapshots the page with WebKit at 1x and 2x:
//
//   src-tauri/dmg/background.png      (1x, windowSize)
//   src-tauri/dmg/background@2x.png   (2x)
//   src-tauri/dmg/background.tiff     (both, via tiffutil; what the DMG uses,
//                                      so Finder picks the sharp one on Retina)
//
// macOS only (AppKit + WebKit + tiffutil ship with the OS); no package
// dependency. Re-run it after editing background.html or the dmg layout in
// tauri.conf.json, and commit the three outputs with the source.

import AppKit
import Foundation
import WebKit

// Finder draws an icon's label this far below the icon centre (128 pt icon,
// 16 pt label text — the bundler's create-dmg defaults). Measured on a
// mounted DMG; the label pad in the background is placed by it.
let labelOffset = 72

let desktop = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
let tauriDir = desktop.appendingPathComponent("src-tauri")
let outDir = tauriDir.appendingPathComponent("dmg")

func fail(_ message: String) -> Never {
  FileHandle.standardError.write(Data("render-dmg-background: \(message)\n".utf8))
  exit(1)
}

struct Layout {
  let width: Int
  let height: Int
  let app: (Int, Int)
  let apps: (Int, Int)
}

func readLayout() -> Layout {
  let confURL = tauriDir.appendingPathComponent("tauri.conf.json")
  guard let data = try? Data(contentsOf: confURL),
    let conf = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
    let bundle = conf["bundle"] as? [String: Any],
    let macOS = bundle["macOS"] as? [String: Any],
    let dmg = macOS["dmg"] as? [String: Any],
    let size = dmg["windowSize"] as? [String: Int],
    let app = dmg["appPosition"] as? [String: Int],
    let apps = dmg["applicationFolderPosition"] as? [String: Int],
    let w = size["width"], let h = size["height"],
    let ax = app["x"], let ay = app["y"], let fx = apps["x"], let fy = apps["y"]
  else { fail("tauri.conf.json has no complete bundle.macOS.dmg layout") }
  return Layout(width: w, height: h, app: (ax, ay), apps: (fx, fy))
}

let layout = readLayout()
let sourceURL = outDir.appendingPathComponent("background.html")
guard let source = try? String(contentsOf: sourceURL, encoding: .utf8), source.contains("/*LAYOUT*/") else {
  fail("\(sourceURL.path) is missing or has no /*LAYOUT*/ marker")
}
let vars = """
  :root { --w: \(layout.width)px; --h: \(layout.height)px; \
  --app-x: \(layout.app.0)px; --app-y: \(layout.app.1)px; \
  --apps-x: \(layout.apps.0)px; --apps-y: \(layout.apps.1)px; --label-dy: \(labelOffset)px; }
  """
let html = source.replacingOccurrences(of: "/*LAYOUT*/", with: vars)

final class Renderer: NSObject, WKNavigationDelegate {
  let view: WKWebView
  let window: NSWindow

  override init() {
    let frame = NSRect(x: 0, y: 0, width: layout.width, height: layout.height)
    view = WKWebView(frame: frame, configuration: WKWebViewConfiguration())
    window = NSWindow(contentRect: frame, styleMask: [.borderless], backing: .buffered, defer: false)
    super.init()
    window.contentView = view
    window.setFrameOrigin(NSPoint(x: -10_000, y: -10_000))
    window.orderBack(nil)
    view.navigationDelegate = self
    view.loadHTMLString(html, baseURL: outDir)
  }

  func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
    // Wait for fonts, then two frames, so the snapshot is the settled layout.
    webView.evaluateJavaScript(
      "document.fonts.ready.then(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))))"
    ) { _, _ in
      self.snapshot(scale: 1) {
        self.snapshot(scale: 2) {
          self.combine()
          NSApp.terminate(nil)
        }
      }
    }
  }

  func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
    fail("load failed: \(error)")
  }

  func snapshot(scale: Int, then next: @escaping () -> Void) {
    let config = WKSnapshotConfiguration()
    config.rect = view.bounds
    config.snapshotWidth = NSNumber(value: layout.width * scale)
    view.takeSnapshot(with: config) { image, error in
      guard let image else { fail("snapshot failed: \(String(describing: error))") }
      let pixelsWide = layout.width * scale
      let pixelsHigh = layout.height * scale
      guard let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: pixelsWide, pixelsHigh: pixelsHigh,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)
      else { fail("cannot allocate a \(pixelsWide)x\(pixelsHigh) bitmap") }
      NSGraphicsContext.saveGraphicsState()
      NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
      image.draw(in: NSRect(x: 0, y: 0, width: pixelsWide, height: pixelsHigh))
      NSGraphicsContext.restoreGraphicsState()
      // Point size stays the window size, so the 2x file carries 144 dpi.
      rep.size = NSSize(width: layout.width, height: layout.height)
      let name = scale == 1 ? "background.png" : "background@2x.png"
      guard let png = rep.representation(using: .png, properties: [:]) else { fail("png encode failed") }
      do {
        try png.write(to: outDir.appendingPathComponent(name))
      } catch {
        fail("write \(name): \(error)")
      }
      print("wrote \(outDir.appendingPathComponent(name).path) (\(pixelsWide)x\(pixelsHigh))")
      next()
    }
  }

  func combine() {
    let task = Process()
    task.executableURL = URL(fileURLWithPath: "/usr/bin/tiffutil")
    task.arguments = [
      "-cathidpicheck",
      outDir.appendingPathComponent("background.png").path,
      outDir.appendingPathComponent("background@2x.png").path,
      "-out", outDir.appendingPathComponent("background.tiff").path,
    ]
    do {
      try task.run()
      task.waitUntilExit()
    } catch {
      fail("tiffutil: \(error)")
    }
    if task.terminationStatus != 0 { fail("tiffutil exited \(task.terminationStatus)") }
    print("wrote \(outDir.appendingPathComponent("background.tiff").path)")
  }
}

let app = NSApplication.shared
app.setActivationPolicy(.prohibited)
let renderer = Renderer()
app.run()
