import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"

export function ConfirmDialog({
  open, onOpenChange, title, text, confirmLabel, danger, busy, onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  text: string
  confirmLabel: string
  danger?: boolean
  busy?: boolean
  onConfirm: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md rounded-[20px] bg-white p-7">
        <DialogHeader className="gap-2 text-left">
          <DialogTitle className="font-display text-xl font-semibold tracking-tight">{title}</DialogTitle>
          <DialogDescription className="text-[15px] leading-relaxed text-ink-2">{text}</DialogDescription>
        </DialogHeader>
        <DialogFooter className="mt-4 flex-row justify-end gap-2.5">
          <button type="button" onClick={() => onOpenChange(false)} className="h-11 rounded-[10px] border border-line px-4 text-[15px] font-medium hover:bg-accent">
            Отмена
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={onConfirm}
            className={`h-11 rounded-[10px] px-4 text-[15px] font-semibold text-white disabled:opacity-60 ${danger ? "bg-down hover:bg-down-text" : "bg-cobalt hover:bg-cobalt-dark"}`}
          >
            {busy ? "Подождите…" : confirmLabel}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
