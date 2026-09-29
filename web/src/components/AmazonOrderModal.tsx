import { useEffect, useState } from 'react'
import { ShoppingCart } from 'lucide-react'
import { api, euro } from '../lib/api'
import type { Order } from '../lib/types'
import { Button, Modal } from './ui'

type Config = {
  enabled: boolean
  mode?: 'test' | 'production'
  readyToOrder?: boolean
  shipTo?: { company: string; street: string; postalCode: string; city: string; country: string }
}

export default function AmazonOrderModal({ order, onClose, onOrdered }: {
  order: Order
  onClose: () => void
  onOrdered: (order: Order) => void
}) {
  const [config, setConfig] = useState<Config | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { api<Config>('/amazon/punchout/config').then(setConfig).catch(err => setError((err as Error).message)) }, [])
  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      onOrdered(await api<Order>(`/orders/${order.id}/amazon/submit`, { method: 'POST', body: '{}' }))
    } catch (err) {
      setError((err as Error).message)
      setBusy(false)
    }
  }
  return <Modal title={`Amazon Business · ${order.number}`} wide onClose={onClose} footer={<>
    <Button variant="ghost" onClick={onClose} disabled={busy}>Schließen</Button>
    <Button variant="primary" onClick={() => void submit()} disabled={busy || !config?.readyToOrder}>
      <ShoppingCart size={16}/> {busy ? 'Wird übertragen …' : config?.mode === 'test' ? 'Testbestellung senden' : 'Jetzt verbindlich bestellen'}
    </Button>
  </>}>
    {error && <div className="notice" role="alert">{error}</div>}
    {config?.mode === 'test' && <div className="notice">Testmodus: Amazon storniert Testbestellungen automatisch. Vor echtem Einkauf muss die Integration auf Aktiv umgestellt werden.</div>}
    {config && !config.readyToOrder && <div className="notice">Bestell-URL oder Lieferadresse fehlen. Die Bestellung kann noch nicht gesendet werden.</div>}
    {config?.shipTo && <p><strong>Lieferung an:</strong> {config.shipTo.company}, {config.shipTo.street}, {config.shipTo.postalCode} {config.shipTo.city}, {config.shipTo.country}</p>}
    <div className="table-wrap"><table><thead><tr><th>Artikel</th><th>Menge</th><th>Einzelpreis</th><th>Summe</th></tr></thead><tbody>
      {order.lines.map(line => <tr key={line.id}><td>{line.description}</td><td>{line.quantity} {line.unit}</td><td>{euro(line.unitPriceCents)}</td><td>{euro(Math.round(line.quantity * line.unitPriceCents))}</td></tr>)}
    </tbody></table></div>
    <p><strong>Summe: {euro(order.totalCents)}</strong></p>
    <p className="cell-sub">Die angezeigten Warenkorbpreise stammen aus Amazon Business. Versand und Steuern können in der Amazon-Bestätigung abweichen.</p>
  </Modal>
}
