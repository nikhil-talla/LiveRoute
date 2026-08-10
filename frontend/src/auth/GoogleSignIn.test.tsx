import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";

import type { LiveRouteApi } from "../api/client";
import type { Session } from "../api/types";
import { GoogleSignIn } from "./GoogleSignIn";

const session: Session = {
  user: {
    user_id: "11111111-1111-4111-8111-111111111111",
    display_name: "Nikhil",
    default_time_zone_name: "America/Chicago",
  },
  csrf_token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  idle_expires_at_unix_ms: 1_786_291_200_000,
  absolute_expires_at_unix_ms: 1_788_883_200_000,
};

function unused(): never {
  throw new Error("unexpected API operation");
}

function api(overrides: Partial<LiveRouteApi> = {}): LiveRouteApi {
  return {
    createGoogleLoginNonce: async () => ({
      nonce: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
      expires_at_unix_ms: Date.now() + 300_000,
    }),
    authenticateWithGoogle: async () => session,
    getSession: unused,
    listTrips: unused,
    getTrip: unused,
    createTrip: unused,
    activateTrip: unused,
    createWebSocketTicket: unused,
    resolvePlace: unused,
    createPlace: unused,
    ...overrides,
  };
}

describe("GoogleSignIn", () => {
  afterEach(() => {
    delete window.google;
  });

  it("keeps sign-in disabled when the public client id is absent", () => {
    render(<GoogleSignIn api={api()} clientId="" onAuthenticated={vi.fn()} />);

    expect(
      screen.getByRole("button", { name: "Sign in with Google" }),
    ).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent("not configured");
  });

  it("binds the backend nonce and selected timezone to Google login", async () => {
    let credentialCallback:
      ((response: { credential?: string }) => void) | undefined;
    const initialize = vi.fn(
      (configuration: {
        callback: (response: { credential?: string }) => void;
      }) => {
        credentialCallback = configuration.callback;
      },
    );
    const renderButton = vi.fn((parent: HTMLElement) => {
      const button = document.createElement("button");
      button.textContent = "Google account";
      parent.append(button);
    });
    window.google = { accounts: { id: { initialize, renderButton } } };
    const authenticateWithGoogle = vi.fn(async () => session);
    const onAuthenticated = vi.fn();

    render(
      <GoogleSignIn
        api={api({ authenticateWithGoogle })}
        clientId="web-client.apps.googleusercontent.com"
        onAuthenticated={onAuthenticated}
      />,
    );

    await screen.findByRole("button", { name: "Google account" });
    expect(initialize).toHaveBeenCalledWith(
      expect.objectContaining({
        client_id: "web-client.apps.googleusercontent.com",
        nonce: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
      }),
    );

    fireEvent.change(screen.getByLabelText("Home timezone"), {
      target: { value: "America/Chicago" },
    });
    await act(async () => {
      credentialCallback?.({ credential: "header.payload.signature" });
    });

    await waitFor(() =>
      expect(authenticateWithGoogle).toHaveBeenCalledWith({
        credential: "header.payload.signature",
        default_time_zone_name: "America/Chicago",
      }),
    );
    expect(onAuthenticated).toHaveBeenCalledWith(session);
  });
});
