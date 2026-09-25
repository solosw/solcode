/** Native persistence adapter consumed by the official onboarding continuation. */
import type { DesktopSetupWizardInput, DesktopSetupWizardSelection } from './setup-wizard-contract.ts'
export const SETUP_ONBOARDING_CHANNEL = 'dsh-desktop:setup-onboarding'
export interface DesktopOnboardingSnapshot {
  required: boolean
  accountPending?: boolean
  /** Preferences saved in this process, awaiting an explicit final application. */
  restartPending?: boolean
  profile: string
  edition: 'desktop' | 'next'
  input: DesktopSetupWizardInput
  computerUse?: boolean
}
export interface DesktopOnboardingBridge {
  read(): Promise<DesktopOnboardingSnapshot | null>
  finish(profile: string, selection?: DesktopSetupWizardSelection & { computerUse?: boolean }): Promise<void>
  dismissAccount(profile: string): Promise<void>
  applyPending?(profile: string): Promise<void>
}
