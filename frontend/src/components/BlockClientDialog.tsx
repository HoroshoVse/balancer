import { useState } from "react"
import { toast } from "sonner"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { errorMessage } from "@/lib/api"
import { withDenied } from "@/lib/clients"
import { countryName } from "@/lib/countries"
import { updateLB } from "@/lib/lbActions"
import type { ClientStat, LoadBalancer } from "@/types"

/** Asks before putting an address on the balancer's deny list, then does it. */
export function BlockClientDialog({
  lb, target, onClose, onChanged,
}: {
  lb: LoadBalancer
  target: ClientStat | null
  onClose: () => void
  onChanged: () => void
}) {
  const [busy, setBusy] = useState(false)

  const block = async (c: ClientStat) => {
    setBusy(true)
    try {
      await updateLB(lb, (x) => ({ ...x, access_deny: withDenied(x.access_deny, c.ip) }))
      toast.success(`${c.ip} в списке запрещённых: его подключения отклоняются`)
      onChanged()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setBusy(false)
      onClose()
    }
  }

  return (
    <ConfirmDialog
      open={!!target}
      onOpenChange={(open) => !open && !busy && onClose()}
      title={`Заблокировать ${target?.ip ?? ""}?`}
      text={`Адрес${target?.country ? ` (${countryName(target.country)})` : ""} попадёт в список запрещённых этого балансировщика, и его подключения будут отклоняться. Балансировщик на мгновение перезапустится. ${target?.internal ? "Это внутренний адрес: проверьте, что он не принадлежит вашему серверу, проверке узлов или другому балансировщику. " : ""}Убрать адрес можно в конструкторе, в разделе доступа.`}
      confirmLabel="Заблокировать"
      danger
      busy={busy}
      onConfirm={() => target && block(target)}
    />
  )
}
