/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_LIVEROUTE_API_ORIGIN?: string;
  readonly VITE_GOOGLE_WEB_CLIENT_ID?: string;
  readonly VITE_MAPBOX_PUBLIC_ACCESS_TOKEN?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
