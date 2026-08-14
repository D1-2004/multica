import "@testing-library/jest-dom/vitest";

// Node 25 exposes its own global localStorage. In a jsdom test that object is
// not the browser storage at window.localStorage, even though browsers expose
// both names as the same object. Keep the test environment browser-accurate.
Object.defineProperty(globalThis, "localStorage", {
  configurable: true,
  value: window.localStorage,
});

// jsdom doesn't provide matchMedia; useIsMobile() relies on it.
if (typeof window.matchMedia !== "function") {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}

// jsdom doesn't provide ResizeObserver; stub it so components that rely on it
// (e.g. input-otp) can render in tests.
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

// jsdom doesn't implement elementFromPoint; input-otp uses it internally.
if (typeof document.elementFromPoint !== "function") {
  document.elementFromPoint = () => null;
}

// jsdom has no layout, so it doesn't implement scrollIntoView; list components
// that keep a keyboard cursor in view (e.g. the thread navigator) call it.
if (typeof Element.prototype.scrollIntoView !== "function") {
  Element.prototype.scrollIntoView = () => {};
}
