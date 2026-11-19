<!--
  RecoverVerify — landing view for the account-recovery magic-link.

  The user arrives here after clicking the link in their recovery email.
  App.svelte has already parsed the ?token= query param from the landing URL
  (/recover/verify?token=…) and written it to the pendingVerifyToken store.

  Flow:
    1. On mount: call GET /auth/recover/verify?token=… to validate the token
       and fetch the WebAuthn PublicKeyCredentialCreationOptions.
    2. Call navigator.credentials.create() with the decoded options.
    3. POST the serialized attestation to /auth/recover/finish?token=….
    4. On success: call GET /api/me to confirm the session, set currentUser,
       clear pendingVerifyToken, and call `onverified` (#0120) so App.svelte
       hands control from this standalone view over to the hash router.

  This view renders OUTSIDE the hash router (#0120 Notes: /recover/verify is
  a real path, a landing target for an emailed link, not a hash route), so it
  cannot call the router's own navigate()/replace() directly — App.svelte
  hasn't wired the router up yet while this is showing. `onverified` and
  `ongotologin` are the two ways control hands back to App.svelte.
-->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { pendingVerifyToken, currentUser } from '../lib/stores';
  import { recoverVerify, recoverFinish, getMe, ApiError } from '../lib/api';
  import {
    toPublicKeyCreationOptions,
    serializeAttestation,
  } from '../lib/webauthn';
  import { get } from 'svelte/store';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import AuthBrand from '../lib/AuthBrand.svelte';
  import StepIndicator from '../lib/StepIndicator.svelte';
  import {
    verifySteps,
    newLinkPath,
    ACTION_LABELS,
    PASSKEY_PROMPT_HELP,
    type NextAction,
  } from '../lib/authScreens';

  interface Props {
    /** Called once the session is confirmed — hands control to App.svelte, which enters the hash router at '/'. */
    onverified: () => void;
    /** Called from the error state's actions. `path` defaults to /login. */
    ongotologin: (path?: string) => void;
  }

  let { onverified, ongotologin }: Props = $props();

  type Status = 'verifying' | 'creating' | 'finishing' | 'error';

  let status = $state<Status>('verifying');
  let errorMessage = $state<string | null>(null);
  let token = $state<string | null>(null);
  // The next action the error state offers inline (#0172).
  let errorAction = $state<NextAction>('sign-in');
  // Whether the link was accepted, so a failure is shown on the right step.
  let linkAccepted = false;
  let panelEl: HTMLDivElement | undefined = $state();

  const steps = $derived(verifySteps(status, linkAccepted ? 1 : 0));

  async function run() {
    status = 'verifying';
    errorMessage = null;
    errorAction = 'sign-in';
    linkAccepted = false;
    token = get(pendingVerifyToken);
    if (!token) {
      status = 'error';
      errorAction = 'new-link';
      errorMessage = 'No recovery token found. Please check your email link and try again.';
      return;
    }

    // Step 1 — fetch creation options.
    let serverOptions;
    try {
      serverOptions = await recoverVerify(token);
    } catch (err) {
      status = 'error';
      errorAction = 'retry';
      if (err instanceof ApiError) {
        if (err.status === 410 || err.status === 400) {
          errorAction = 'new-link';
          errorMessage =
            'This recovery link has expired or is invalid. Please request a new one.';
        } else if (err.status === 401) {
          errorAction = 'new-link';
          errorMessage = 'Recovery failed. Please request a new recovery email.';
        } else {
          errorMessage = `Recovery error: ${err.message}`;
        }
      } else {
        errorMessage = 'An unexpected error occurred. Please try again.';
      }
      return;
    }

    linkAccepted = true;
    await create(serverOptions);
  }

  /** Steps 2-4: the passkey prompt, finish, then the session. */
  async function create(serverOptions: Awaited<ReturnType<typeof recoverVerify>>) {
    const tok = token;
    if (!tok) return;

    // Step 2 — run the browser credential-creation ceremony.
    status = 'creating';
    let credential: PublicKeyCredential | null = null;
    try {
      const publicKey = toPublicKeyCreationOptions(serverOptions.publicKey);
      credential = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null;
    } catch (err) {
      status = 'error';
      errorAction = 'retry';
      if (err instanceof DOMException) {
        if (err.name === 'NotAllowedError') {
          errorMessage =
            'Passkey creation was cancelled. Please try again when you are ready.';
        } else if (err.name === 'InvalidStateError') {
          errorAction = 'sign-in';
          errorMessage =
            'A passkey for this account is already registered on this device.';
        } else {
          errorMessage = 'The passkey prompt was unavailable. Please try again.';
        }
      } else {
        errorMessage = 'An unexpected error occurred during passkey creation. Please try again.';
      }
      return;
    }

    if (!credential) {
      status = 'error';
      errorAction = 'retry';
      errorMessage = 'No passkey was created. Please try again.';
      return;
    }

    // Step 3 — POST attestation to finish recovery.
    status = 'finishing';
    try {
      const attestation = serializeAttestation(credential);
      await recoverFinish(tok, attestation);
    } catch (err) {
      status = 'error';
      errorAction = 'retry';
      if (err instanceof ApiError) {
        if (err.status === 400) {
          errorAction = 'new-link';
          errorMessage =
            'Recovery could not be completed. The link may have expired — please request a new one.';
        } else {
          errorMessage = `Recovery failed: ${err.message}`;
        }
      } else {
        errorMessage = 'Could not reach the server. Check your connection and try again.';
      }
      return;
    }

    // Step 4 — confirm session, set user, hand off to the hash router.
    try {
      const user = await getMe();
      currentUser.set(user);
      pendingVerifyToken.set(null);
      // Fixes the PATHNAME back to '/' — separate from the hash router's own
      // '/' that enterApp() will set next, since this is still a real path
      // (/recover/verify), not a hash route (#0120 Notes).
      history.replaceState({}, '', '/');
      onverified();
    } catch {
      status = 'error';
      errorAction = 'sign-in';
      errorMessage =
        'Your account was recovered but we could not load your profile. Try signing in.';
    }
  }

  // Try again re-runs verify, which checks the token first: an expired link
  // fails fast with "Send a new link" before any passkey is created. Focus then
  // returns to the card so a keyboard or screen-reader user is not left on a
  // removed button.
  async function retry() {
    const pending = run();
    await tick();
    panelEl?.focus();
    await pending;
  }

  onMount(run);

  function goToLogin(path?: string) {
    pendingVerifyToken.set(null);
    // Fixes the pathname back to '/' first, so the address bar doesn't end up
    // '/recover/verify#/login' once ongotologin() sets the hash.
    history.replaceState({}, '', '/');
    ongotologin(path);
  }
</script>

<main class="verify-shell">
  <AuthBrand />

  <Panel>
    <div class="card-body" tabindex="-1" bind:this={panelEl}>
    <StepIndicator {steps} />
    {#if status === 'verifying'}
      <p class="text-muted">Checking your recovery link…</p>
    {:else if status === 'creating'}
      <p>Create your new passkey</p>
      <p class="text-muted">{PASSKEY_PROMPT_HELP}</p>
    {:else if status === 'finishing'}
      <p class="text-muted">Finishing account recovery…</p>
    {:else if status === 'error'}
      <p class="text-error" role="alert">{errorMessage}</p>
      <div class="next">
        {#if errorAction === 'retry'}
          <Button variant="primary" onclick={retry}>{ACTION_LABELS.retry}</Button>
        {:else if errorAction === 'new-link'}
          <Button variant="primary" onclick={() => goToLogin(newLinkPath('recover'))}>
            {ACTION_LABELS['new-link']}
          </Button>
        {/if}
        <Button variant={errorAction === 'sign-in' ? 'primary' : 'subtle'} onclick={() => goToLogin()}>
          {ACTION_LABELS['sign-in']}
        </Button>
      </div>
    {/if}
    </div>
  </Panel>
</main>

<style>
  .verify-shell {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
    width: 100%;
  }
  .card-body:focus {
    outline: none;
  }
  .next {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-3);
  }

  @media (max-width: 480px) {
    .verify-shell {
      margin-top: var(--space-5);
    }
  }
</style>
