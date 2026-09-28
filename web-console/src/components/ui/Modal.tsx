import React, { useEffect, useId, useRef } from 'react'
import { X } from 'lucide-react'

export type ModalSize = 'sm' | 'md' | 'lg' | 'xl' | 'full'

interface ModalProps {
  open: boolean
  onClose: () => void
  title: React.ReactNode
  size?: ModalSize
  children: React.ReactNode
  footer?: React.ReactNode
  description?: React.ReactNode
  /** Escape and backdrop clicks are ignored while false, so a caller can hold a
   *  dialog open through a local save — or, for a surface that owns every
   *  keystroke (a live terminal, a remote desktop), keep Escape off entirely.
   *  The close button stays: it is a deliberate click, not an ambient gesture. */
  dismissible?: boolean
}

/**
 * Native <dialog>. Top layer, focus containment and the Escape key all come
 * from showModal(); only scroll lock and focus restore are ours.
 * ponytail: one dialog at a time — nested modals need a stack, add when asked.
 */
export const Modal: React.FC<ModalProps> = ({
  open,
  onClose,
  title,
  size = 'md',
  children,
  footer,
  description,
  dismissible = true,
}) => {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const titleId = useId()
  const descriptionId = useId()

  useEffect(() => {
    if (!open) return
    const dialog = dialogRef.current
    if (!dialog) return

    const restoreTo = document.activeElement as HTMLElement | null
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    if (!dialog.open) dialog.showModal()

    return () => {
      document.body.style.overflow = previousOverflow
      // Unmounting takes the dialog out of the top layer, so no close() call.
      restoreTo?.focus?.()
    }
  }, [open])

  if (!open) return null

  return (
    <dialog
      ref={dialogRef}
      className={`ui-modal ui-modal-${size}`}
      aria-labelledby={titleId}
      aria-describedby={description ? descriptionId : undefined}
      // Escape and the close button share one path back to the caller: the
      // native close event, which fires for both.
      onClose={onClose}
      onCancel={(e) => {
        if (!dismissible) e.preventDefault()
      }}
      onClick={(e) => {
        if (dismissible && e.target === dialogRef.current) onClose()
      }}
    >
      <div className="ui-modal-panel">
        <header className="ui-modal-header">
          <div className="ui-modal-heading">
            <h2 className="ui-modal-title" id={titleId}>
              {title}
            </h2>
            {description && (
              <p className="ui-modal-description" id={descriptionId}>
                {description}
              </p>
            )}
          </div>
          <button
            type="button"
            className="btn btn-icon ui-modal-close"
            onClick={onClose}
            aria-label="Close dialog"
          >
            <X size={18} />
          </button>
        </header>
        <div className="ui-modal-body">{children}</div>
        {footer && <footer className="ui-modal-footer">{footer}</footer>}
      </div>
    </dialog>
  )
}
