import React from 'react'
import { RefreshCw } from 'lucide-react'
import { Modal } from './Modal'

interface ConfirmDialogProps {
  open: boolean
  title: React.ReactNode
  message: React.ReactNode
  confirmLabel?: string
  pending?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export const ConfirmDialog: React.FC<ConfirmDialogProps> = ({
  open,
  title,
  message,
  confirmLabel = 'Confirm',
  pending = false,
  onConfirm,
  onCancel,
}) => (
  <Modal
    open={open}
    onClose={onCancel}
    title={title}
    size="sm"
    // A local save owns Escape until it settles: nothing here can close.
    dismissible={!pending}
    footer={
      <>
        <button type="button" className="btn btn-secondary" onClick={onCancel} disabled={pending}>
          Cancel
        </button>
        <button type="button" className="btn btn-primary" onClick={onConfirm} disabled={pending}>
          {pending && <RefreshCw size={14} className="spinning" />}
          <span>{pending ? 'Working...' : confirmLabel}</span>
        </button>
      </>
    }
  >
    <p className="confirm-message">{message}</p>
  </Modal>
)
