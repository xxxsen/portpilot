"use client";

import type { ReactNode } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import * as AlertPrimitive from "@radix-ui/react-alert-dialog";
import { X } from "lucide-react";

export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  lockClose = false,
  side = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
  lockClose?: boolean;
  side?: boolean;
}) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="modal-backdrop" />
        <DialogPrimitive.Content
          className={side ? "modal drawer" : "modal"}
          onEscapeKeyDown={(event) => { if (lockClose) event.preventDefault(); }}
          onPointerDownOutside={(event) => { if (lockClose) event.preventDefault(); }}
        >
          <div className="modal-header">
            <div>
              <DialogPrimitive.Title>{title}</DialogPrimitive.Title>
              {description && <DialogPrimitive.Description>{description}</DialogPrimitive.Description>}
            </div>
            {!lockClose && (
              <DialogPrimitive.Close className="icon-button" aria-label="关闭" title="关闭">
                <X size={18} />
              </DialogPrimitive.Close>
            )}
          </div>
          {children}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

export function AlertDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  busy,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel: string;
  busy?: boolean;
  onConfirm: () => void;
}) {
  return (
    <AlertPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <AlertPrimitive.Portal>
        <AlertPrimitive.Overlay className="modal-backdrop" />
        <AlertPrimitive.Content className="modal alert-modal">
          <div className="modal-header">
            <div>
              <AlertPrimitive.Title>{title}</AlertPrimitive.Title>
              <AlertPrimitive.Description>{description}</AlertPrimitive.Description>
            </div>
          </div>
          <div className="modal-actions">
            <AlertPrimitive.Cancel className="secondary-button" disabled={busy}>取消</AlertPrimitive.Cancel>
            <AlertPrimitive.Action className="danger-button" disabled={busy} onClick={onConfirm}>
              {busy ? "处理中..." : confirmLabel}
            </AlertPrimitive.Action>
          </div>
        </AlertPrimitive.Content>
      </AlertPrimitive.Portal>
    </AlertPrimitive.Root>
  );
}
