import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './styles.css'

const rootEl = document.getElementById('root')
if (!rootEl) {
  // 索引页被改坏时给一句能读懂的话，而不是静默白屏。
  throw new Error('#root not found in index.html')
}

ReactDOM.createRoot(rootEl).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
