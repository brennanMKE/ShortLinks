<!--
  StepIndicator (#0172) — "Email confirmed → Create passkey → Done" on the
  verify screens. Each step carries its state as text for assistive tech, not
  only colour.
-->
<script lang="ts">
  import type { Step } from './authScreens';

  let { steps }: { steps: Step[] } = $props();

  const STATE_TEXT: Record<Step['state'], string> = {
    done: 'completed',
    current: 'current step',
    todo: 'not started',
    failed: 'needs attention',
  };
</script>

<ol class="steps" aria-label="Progress">
  {#each steps as step, i (step.label)}
    <li class="step {step.state}" aria-current={step.state === 'current' ? 'step' : undefined}>
      <span class="dot" aria-hidden="true">{step.state === 'done' ? '✓' : step.state === 'failed' ? '!' : i + 1}</span>
      <span class="label">{step.label}</span>
      <span class="sr-only">, {STATE_TEXT[step.state]}</span>
    </li>
  {/each}
</ol>

<style>
  .steps {
    display: flex;
    justify-content: space-between;
    gap: var(--space-1);
    list-style: none;
    margin: 0 0 var(--space-4);
    padding: 0;
  }
  .step {
    flex: 1 1 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-1);
    text-align: center;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .dot {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: var(--fs-sm);
    font-weight: 600;
    border: var(--border-w) solid var(--border);
    background: var(--bg-panel);
  }
  .step.done .dot {
    background: var(--success);
    border-color: var(--success);
    color: #fff;
  }
  .step.current {
    color: var(--text);
    font-weight: 600;
  }
  .step.current .dot {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--accent-text);
  }
  .step.failed {
    color: var(--danger);
  }
  .step.failed .dot {
    border-color: var(--danger);
    background: var(--danger-subtle);
    color: var(--danger);
  }
</style>
