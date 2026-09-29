import { FormEvent, useState } from 'react'
import { FileUp, Plus, X } from 'lucide-react'
import { api, euro } from '../lib/api'
import type { Product, Supplier } from '../lib/types'
import { Badge, Button, Field, Modal } from './ui'

type OfferLine = {
  productId?: number
  description: string
  quantity: number
  unit: string
  estimatedPriceCents: number
  purchaseUrl: string
  supplierSku?: string
  matchMethod?: string
  matchConfidence?: number
  createProduct: boolean
  sku: string
  productName: string
  manufacturer: string
}

export type OfferPreview = {
  sourceFileName: string
  pageCount: number
  extractedCharacters: number
  ocrUsed: boolean
  supplierId?: number
  supplierName?: string
  offerNumber: string
  offerDate?: string
  currency: string
  documentTotalCents: number
  recognizedTotalCents: number
  warnings: string[]
  lines: Array<Omit<OfferLine, 'createProduct' | 'sku' | 'productName' | 'manufacturer'>>
}

const blankLine = (): OfferLine => ({ description: '', quantity: 1, unit: 'Stk.', estimatedPriceCents: 0, purchaseUrl: '', createProduct: false, sku: '', productName: '', manufacturer: '' })

export default function OfferRequisitionModal({ suppliers, products, onClose, onSaved }: {
  suppliers: Supplier[]
  products: Product[]
  onClose: () => void
  onSaved: () => void
}) {
  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<OfferPreview | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [supplierId, setSupplierId] = useState(0)
  const [offerNumber, setOfferNumber] = useState('')
  const [currency, setCurrency] = useState('EUR')
  const [title, setTitle] = useState('')
  const [costCenter, setCostCenter] = useState('')
  const [neededBy, setNeededBy] = useState('')
  const [justification, setJustification] = useState('')
  const [lines, setLines] = useState<OfferLine[]>([])

  const analyze = async (event: FormEvent) => {
    event.preventDefault()
    if (!file) return
    if (file.size > 12 * 1024 * 1024) { setError('PDF darf maximal 12 MB groß sein.'); return }
    setLoading(true); setError('')
    try {
      const form = new FormData()
      form.append('file', file)
      const result = await api<OfferPreview>('/requisitions/offer-preview', { method: 'POST', body: form })
      setPreview(result)
      setSupplierId(result.supplierId || 0)
      setOfferNumber(result.offerNumber)
      setCurrency(result.currency)
      setTitle(result.offerNumber ? `Bedarf aus Angebot ${result.offerNumber}` : `Bedarf aus ${result.sourceFileName}`)
      setLines(result.lines.length ? result.lines.map(line => ({ ...line, createProduct: false, sku: line.supplierSku || '', productName: line.description, manufacturer: result.supplierName?.toLowerCase().includes('adam hall') ? 'Adam Hall' : '' })) : [blankLine()])
    } catch (caught) { setError((caught as Error).message) }
    finally { setLoading(false) }
  }
  const update = (index: number, patch: Partial<OfferLine>) => setLines(current => current.map((line, i) => i === index ? { ...line, ...patch } : line))
  const selectProduct = (index: number, id: number) => {
    const product = products.find(row => row.id === id)
    update(index, { productId: product?.id, createProduct: false, description: product?.name || lines[index].description, unit: product?.unit || lines[index].unit })
  }
  const save = async (event: FormEvent) => {
    event.preventDefault()
    if (!preview) return
    if (!supplierId) { setError('Bitte Lieferant auswählen.'); return }
    setLoading(true); setError('')
    try {
      await api('/requisitions/from-offer', { method: 'POST', body: JSON.stringify({
        title, costCenter, neededBy: neededBy ? new Date(`${neededBy}T12:00:00`).toISOString() : null,
        justification,
        supplierId, offerNumber, sourceFileName: preview.sourceFileName, currency,
        lines: lines.map(({ matchMethod, matchConfidence, supplierSku, ...line }) => line),
      }) })
      onSaved()
    } catch (caught) { setError((caught as Error).message) }
    finally { setLoading(false) }
  }

  return <Modal title={preview ? 'Angebot prüfen und Bedarf anlegen' : 'Bedarf aus Angebots-PDF'} wide onClose={onClose} footer={<>
    <Button variant="ghost" onClick={onClose} disabled={loading}>Abbrechen</Button>
    <Button variant="primary" type="submit" form={preview ? 'offer-requisition-form' : 'offer-upload-form'} disabled={loading || (!preview && !file)}>{loading ? 'Bitte warten …' : preview ? 'Bedarf als Entwurf speichern' : 'PDF analysieren'}</Button>
  </>}>
    {error && <div className="notice" role="alert">{error}</div>}
    {!preview ? <form id="offer-upload-form" onSubmit={analyze}>
      <Field label="Angebots-PDF"><input type="file" accept="application/pdf,.pdf" required onChange={event => setFile(event.target.files?.[0] || null)} /></Field>
      <p className="import-hint">Maximal 12 MB und 100 Seiten; gescannte PDFs maximal 20 Seiten. Die Texterkennung läuft lokal. JEV hilft bei der Katalogzuordnung. Das PDF wird nicht gespeichert.</p>
      {loading && <div className="empty" role="status"><FileUp size={16} /> Angebot und Positionen werden analysiert …</div>}
    </form> : <form id="offer-requisition-form" onSubmit={save}>
      <div className="receipt-inventory">
        <div className="receipt-inventory-heading"><div><strong>{preview.sourceFileName}</strong><span>{preview.pageCount} Seite(n) · {preview.ocrUsed ? 'OCR' : 'Textebene'} · {preview.extractedCharacters} Zeichen</span></div><Badge tone={preview.warnings.length ? 'amber' : 'green'}>{preview.warnings.length ? `${preview.warnings.length} Prüfhinweis(e)` : 'Bitte prüfen'}</Badge></div>
        {preview.documentTotalCents > 0 && <p>Angebot: {euro(preview.documentTotalCents)} · erkannte Positionen: {euro(preview.recognizedTotalCents)}</p>}
        {preview.warnings.map((warning, index) => <span key={`${index}-${warning}`}>• {warning}</span>)}
      </div>
      <div className="form-grid">
        {currency !== 'EUR' && <div className="notice" role="status">Bedarfe werden in EUR geführt. Bitte Preise manuell in EUR umrechnen und danach EUR auswählen.</div>}
        <Field label="Lieferant"><select required value={supplierId} onChange={event => setSupplierId(Number(event.target.value))}><option value="0">Bitte wählen</option>{suppliers.map(supplier => <option key={supplier.id} value={supplier.id}>{supplier.name}</option>)}</select></Field>
        <Field label="Angebotsnummer"><input value={offerNumber} onChange={event => setOfferNumber(event.target.value)} /></Field>
        <Field label="Währung der eingegebenen Preise"><select value={currency} onChange={event => setCurrency(event.target.value)}><option value="EUR">EUR</option><option value="CHF">CHF</option><option value="USD">USD</option><option value="GBP">GBP</option></select></Field>
        <Field label="Titel"><input required value={title} onChange={event => setTitle(event.target.value)} /></Field>
        <Field label="Kostenstelle"><input value={costCenter} onChange={event => setCostCenter(event.target.value)} /></Field>
        <Field label="Benötigt bis"><input type="date" value={neededBy} onChange={event => setNeededBy(event.target.value)} /></Field>
        <Field label="Begründung" full><textarea value={justification} onChange={event => setJustification(event.target.value)} /></Field>
      </div>
      <h4>Positionen</h4>
      {lines.map((line, index) => <div className="receipt-inventory" key={index}>
        <div className="receipt-inventory-heading"><strong>Position {index + 1}</strong><Button variant="danger" type="button" className="icon" onClick={() => setLines(current => current.filter((_, i) => i !== index))} aria-label={`Position ${index + 1} entfernen`}><X size={16} /></Button></div>
        {line.matchMethod && <p>Zuordnung: {line.matchMethod === 'jev' ? `JEV ${line.matchConfidence}%` : 'Artikelnummer'}. Bitte bestätigen.</p>}
        <div className="form-grid">
          <Field label="Katalogartikel" full><select value={line.productId || ''} onChange={event => selectProduct(index, Number(event.target.value))}><option value="">Kein vorhandener Artikel</option>{products.map(product => <option key={product.id} value={product.id}>{product.sku} · {product.name}</option>)}</select></Field>
          <Field label="Beschreibung" full><input required value={line.description} onChange={event => update(index, { description: event.target.value })} /></Field>
          <Field label="Menge"><input required type="number" min="0.01" step="0.01" value={line.quantity} onChange={event => update(index, { quantity: Number(event.target.value) })} /></Field>
          <Field label="Einheit"><input value={line.unit} onChange={event => update(index, { unit: event.target.value })} /></Field>
          <Field label={`Einzelpreis ${currency}`}><input required type="number" min="0" step="0.01" value={(line.estimatedPriceCents / 100).toString()} onChange={event => update(index, { estimatedPriceCents: Math.round(Number(event.target.value) * 100) })} /></Field>
          <Field label="Einkaufslink"><input type="url" value={line.purchaseUrl} onChange={event => update(index, { purchaseUrl: event.target.value })} /></Field>
          {!line.productId && <Field label="Artikel im Katalog anlegen" full><label><input type="checkbox" checked={line.createProduct} onChange={event => update(index, { createProduct: event.target.checked })} /> Neuen Artikel und Bezugsquelle zusammen mit dem Bedarf speichern</label></Field>}
          {line.createProduct && !line.productId && <><Field label="Neue SKU"><input required maxLength={80} value={line.sku} onChange={event => update(index, { sku: event.target.value })} /></Field><Field label="Neuer Artikelname"><input required maxLength={240} value={line.productName} onChange={event => update(index, { productName: event.target.value })} /></Field><Field label="Hersteller"><input value={line.manufacturer} onChange={event => update(index, { manufacturer: event.target.value })} /></Field></>}
        </div>
      </div>)}
      <Button variant="ghost" type="button" onClick={() => setLines(current => [...current, blankLine()])}><Plus size={15} /> Position ergänzen</Button>
    </form>}
  </Modal>
}
