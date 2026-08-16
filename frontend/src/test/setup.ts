import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

// Browser-only Mapbox GL/SearchBox integration is covered by production builds
// and focused adapter tests. Keep component tests independent of developer-local
// credentials and jsdom's lack of WebGL/custom-element CSS support.
vi.stubEnv("VITE_MAPBOX_PUBLIC_ACCESS_TOKEN", "");
