// Background passkey-autofill ceremony for the sign-in card (#0172).
//
// The AbortController exists BEFORE the feature-detection await, so a stop()
// (leaving the sign-in card, starting an explicit sign-in, unmounting) that
// lands while we are still waiting on isConditionalMediationAvailable() cancels
// this start; otherwise the late-resolving start would fire credentials.get()
// after the caller had already stopped it.

export interface ConditionalLoginDeps {
  /** Feature detection (PublicKeyCredential.isConditionalMediationAvailable). */
  available: () => Promise<boolean>;
  /** Runs the ceremony; must honour `signal`. */
  run: (signal: AbortSignal) => Promise<unknown>;
  /** Called with a ceremony error that was not caused by our own abort. */
  onError: (err: unknown) => void;
}

export function createConditionalLogin(deps: ConditionalLoginDeps) {
  let current: AbortController | null = null;

  async function start(): Promise<void> {
    current?.abort();
    const controller = new AbortController();
    current = controller;
    if (!(await deps.available())) return;
    if (controller.signal.aborted) return;
    try {
      await deps.run(controller.signal);
    } catch (err) {
      if (!controller.signal.aborted) deps.onError(err);
    }
  }

  function stop(): void {
    current?.abort();
    current = null;
  }

  return { start, stop };
}
