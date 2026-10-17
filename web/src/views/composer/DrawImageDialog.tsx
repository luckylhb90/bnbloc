import { useEffect, useRef, useState } from "react";
import { generateImage, useMediaCapabilityView } from "@/lib/media";
import { errorText } from "@/lib/errors";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";
import type { MediaImageAspect } from "@/lib/wire.gen.ts";

// DrawImageDialog is the composer's "draw an image" entry (docs/prd/
// 2026-10-16-voice-and-image-generation.md): the member describes what to
// draw, picks an aspect and a count, and the images land in the composer as
// attachments — preview, edit the caption, send like any file. Nothing to
// configure: the organization's provider is used, the member's own rate.

export interface DrawnImage {
  readonly file: File;
}

export function DrawImageDialog({
  open,
  onClose,
  onImages,
}: {
  readonly open: boolean;
  readonly onClose: () => void;
  /** onImages hands the drawn images to the composer as attachment files. */
  readonly onImages: (images: readonly DrawnImage[]) => void;
}) {
  const [prompt, setPrompt] = useState("");
  const [aspect, setAspect] = useState<MediaImageAspect>("square");
  const [count, setCount] = useState(1);
  const [drawing, setDrawing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [model, setModel] = useState("");
  const promptRef = useRef<HTMLTextAreaElement>(null);
  // The provider's models, when it serves more than one: the picker shows
  // them (the first the default). One model: nothing to choose.
  const imageView = useMediaCapabilityView("image");
  const models = imageView?.models?.length ? imageView.models : [];

  useEffect(() => {
    if (open) {
      setPrompt("");
      setAspect("square");
      setCount(1);
      setModel("");
      setError(null);
      requestAnimationFrame(() => promptRef.current?.focus());
    }
  }, [open]);

  const draw = async () => {
    const p = prompt.trim();
    if (!p || drawing) return;
    setDrawing(true);
    setError(null);
    try {
      const reply = await generateImage(p, { aspect, count, ...(model ? { model } : {}) });
      if (!reply.images || reply.images.length === 0) {
        throw new Error("The provider drew nothing");
      }
      const images: DrawnImage[] = [];
      reply.images.forEach((img, i) => {
        const blob = dataUrlToBlob(img.dataUrl ?? "");
        if (!blob) return;
        const ext = img.mimeType === "image/jpeg" ? "jpg" : img.mimeType === "image/webp" ? "webp" : "png";
        const name = (reply.images?.length ?? 0) > 1 ? `image-${i + 1}.${ext}` : `image.${ext}`;
        images.push({ file: new File([blob], name, { type: img.mimeType }) });
      });
      if (images.length === 0) {
        throw new Error("The images could not be read");
      }
      onImages(images);
      onClose();
    } catch (e) {
      setError(errorText(e, "Drawing failed"));
    } finally {
      setDrawing(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Draw an image"
      description="Describe what to draw; the images join the message as attachments for you to check before sending."
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={drawing}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void draw()} disabled={drawing || !prompt.trim()}>
            {drawing ? "Drawing…" : "Draw"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error && <p className="text-sm text-[var(--color-danger)]">{error}</p>}
        <Field label="What to draw">
          <Textarea
            ref={promptRef as never}
            rows={3}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            placeholder="A poster for Friday's demo, flat colors, large title…"
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                void draw();
              }
            }}
          />
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Shape">
            <Select value={aspect} onChange={(e) => setAspect(e.target.value as MediaImageAspect)}>
              <option value="square">Square</option>
              <option value="landscape">Landscape</option>
              <option value="portrait">Portrait</option>
            </Select>
          </Field>
          <Field label="How many">
            <Select value={String(count)} onChange={(e) => setCount(Number(e.target.value))}>
              {[1, 2, 3, 4].map((n) => (
                <option key={n} value={n}>
                  {n === 1 ? "One" : `${n} options`}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {models.length > 1 && (
          <Field label="Model" hint="The provider serves these; the first is the default.">
            <Select value={model} onChange={(e) => setModel(e.target.value)}>
              <option value="">Default ({models[0]})</option>
              {models.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </Select>
          </Field>
        )}
      </div>
    </Dialog>
  );
}

// dataUrlToBlob decodes a data: URL into a Blob (null when unreadable).
function dataUrlToBlob(dataUrl: string): Blob | null {
  const cut = dataUrl.indexOf(",");
  if (cut < 0) return null;
  const header = dataUrl.slice(0, cut);
  const b64 = dataUrl.slice(cut + 1);
  try {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    const mime = /data:([^;]+)/.exec(header)?.[1] ?? "application/octet-stream";
    return new Blob([bytes], { type: mime });
  } catch {
    return null;
  }
}
