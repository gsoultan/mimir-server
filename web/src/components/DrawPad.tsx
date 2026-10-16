import { useEffect, useImperativeHandle, useRef, type Ref } from "react";

import { toMNIST } from "../lib/digit";

export type DrawPadHandle = { clear(): void; read(): Float32Array };

// A square page to draw on with a finger, pen or mouse.
export function DrawPad({ ref, onStroke }: { ref: Ref<DrawPadHandle>; onStroke?: () => void }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const drawing = useRef(false);

  const ctx = () => canvas.current!.getContext("2d", { willReadFrequently: true })!;
  const clear = () => {
    const c = canvas.current!;
    const g = ctx();
    g.fillStyle = "#FFFFFF";
    g.fillRect(0, 0, c.width, c.height);
  };

  useEffect(clear, []);

  useImperativeHandle(ref, () => ({
    clear,
    read() {
      const c = canvas.current!;
      return toMNIST(ctx().getImageData(0, 0, c.width, c.height).data, c.width, c.height);
    },
  }));

  function point(e: React.PointerEvent<HTMLCanvasElement>) {
    const r = e.currentTarget.getBoundingClientRect();
    return [((e.clientX - r.left) / r.width) * e.currentTarget.width, ((e.clientY - r.top) / r.height) * e.currentTarget.height] as const;
  }

  return (
    <canvas
      ref={canvas}
      width={280}
      height={280}
      aria-label="Drawing area. Draw a single digit."
      className="aspect-square w-full max-w-[340px] cursor-crosshair touch-none rounded-2xl border-2 border-[var(--mantine-color-default-border)]"
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId);
        drawing.current = true;
        const g = ctx();
        g.lineWidth = 20; g.lineCap = "round"; g.lineJoin = "round"; g.strokeStyle = "#16201E";
        g.beginPath();
        g.moveTo(...point(e));
        g.lineTo(...point(e));
        g.stroke();
      }}
      onPointerMove={(e) => {
        if (!drawing.current) return;
        const g = ctx();
        g.lineTo(...point(e));
        g.stroke();
      }}
      onPointerUp={() => {
        if (drawing.current) onStroke?.();
        drawing.current = false;
      }}
      onPointerCancel={() => { drawing.current = false; }}
    />
  );
}
