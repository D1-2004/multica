import { describe, expect, it } from "vitest";
import * as comparisonDialog from "./agent-comparison-dialog";

type LineColorGenerator = (index: number) => string;

function parseOklch(color: string) {
  const match = color.match(
    /^oklch\(([\d.]+) ([\d.]+) ([\d.]+)\)$/,
  );
  if (!match) return null;
  const lightness = Number(match[1]);
  const chroma = Number(match[2]);
  const hue = Number(match[3]);
  const radians = (hue * Math.PI) / 180;
  return {
    lightness,
    a: chroma * Math.cos(radians),
    b: chroma * Math.sin(radians),
  };
}

describe("generateAgentLineColor", () => {
  it("generates a stable, perceptually separated color sequence", () => {
    const generate = (
      comparisonDialog as typeof comparisonDialog & {
        generateAgentLineColor?: LineColorGenerator;
      }
    ).generateAgentLineColor;

    expect(generate).toBeTypeOf("function");

    const colors = generate
      ? Array.from({ length: 8 }, (_, index) => generate(index))
      : [];
    expect(new Set(colors).size).toBe(8);
    expect(generate?.(3)).toBe(generate?.(3));

    const points = colors.map(parseOklch);
    expect(points.every(Boolean)).toBe(true);

    let minimumDistance = Number.POSITIVE_INFINITY;
    for (let left = 0; left < points.length; left++) {
      for (let right = left + 1; right < points.length; right++) {
        const a = points[left];
        const b = points[right];
        if (!a || !b) continue;
        minimumDistance = Math.min(
          minimumDistance,
          Math.hypot(
            a.lightness - b.lightness,
            a.a - b.a,
            a.b - b.b,
          ),
        );
      }
    }
    expect(minimumDistance).toBeGreaterThan(0.12);
  });
});
