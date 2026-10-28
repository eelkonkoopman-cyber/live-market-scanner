//! Linear y-axis with auto-fit (BUILD-PLAN §1.4).
//
//   y = innerTop + innerH × (1 − (price − min)/(max − min))
//
// `scaleMargins` keeps the series off the very top/bottom of the pane (the
// bottom band is where the volume histogram lives).

export class PriceScale {
  /** pane top in px */
  top = 0;
  /** pane height in px */
  height = 0;
  scaleMargins = { top: 0.08, bottom: 0.16 };
  private min = 0;
  private max = 1;

  setRect(top: number, height: number): void {
    this.top = top;
    this.height = height;
  }

  applyAutoRange(min: number, max: number): void {
    if (max <= min) {
      const pad = Math.abs(max) * 0.01 || 1;
      this.min = min - pad;
      this.max = max + pad;
      return;
    }
    this.min = min;
    this.max = max;
  }

  range(): { min: number; max: number } {
    return { min: this.min, max: this.max };
  }

  private innerTop(): number {
    return this.top + this.height * this.scaleMargins.top;
  }
  private innerHeight(): number {
    return this.height * (1 - this.scaleMargins.top - this.scaleMargins.bottom);
  }

  priceToY(price: number): number {
    const frac = (price - this.min) / (this.max - this.min || 1);
    return this.innerTop() + this.innerHeight() * (1 - frac);
  }

  yToPrice(y: number): number {
    const frac = 1 - (y - this.innerTop()) / (this.innerHeight() || 1);
    return this.min + frac * (this.max - this.min);
  }
}
