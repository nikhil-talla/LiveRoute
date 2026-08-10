import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app/App";
import "./styles.css";
import "mapbox-gl/dist/mapbox-gl.css";

const root = document.getElementById("root");

if (root === null) {
  throw new Error("LiveRoute root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
