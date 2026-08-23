import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import './theme.css'
import './index.css'

const basename = window.location.pathname === '/procurement' || window.location.pathname.startsWith('/procurement/') ? '/procurement' : undefined
createRoot(document.getElementById('root')!).render(<StrictMode><BrowserRouter basename={basename}><App /></BrowserRouter></StrictMode>)
