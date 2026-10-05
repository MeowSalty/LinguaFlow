import type { ButtonProps } from 'naive-ui'

// Programmatic dialogs use the app's provider, outside StorageAppearance.
export const storageConfirmationButtons = {
  negativeButtonProps: {
    themeOverrides: {
      textColor: 'var(--lf-text)',
      textColorHover: 'var(--lf-text)',
      textColorFocus: 'var(--lf-text)',
      textColorPressed: 'var(--lf-text)',
      textColorGhost: 'var(--lf-text)',
      textColorGhostHover: 'var(--lf-text)',
      textColorGhostPressed: 'var(--lf-text)',
    },
  } satisfies ButtonProps,
  positiveButtonProps: {
    themeOverrides: {
      colorWarning: 'var(--lf-warning)',
      colorHoverWarning: 'var(--lf-warning)',
      colorFocusWarning: 'var(--lf-warning)',
      colorPressedWarning: 'var(--lf-warning)',
    },
  } satisfies ButtonProps,
}
