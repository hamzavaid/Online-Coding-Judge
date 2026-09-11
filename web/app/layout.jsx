import React from 'react';
import './style.css';
export const metadata={title:'Online Coding Judge',description:'Practice programming with isolated code evaluation.'};
// RootLayout supplies the shared document shell and accessible language metadata.
export default function RootLayout({children}){return <html lang="en"><body>{children}</body></html>;}
