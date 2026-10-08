import { createContext, useContext } from "react";

// RemoteFetchContext is fetch for a remote node's endpoints; the node's page provides one that notices a lost login.
export const RemoteFetchContext = createContext<typeof fetch>((input, init) => fetch(input, init));

export const useRemoteFetch = () => useContext(RemoteFetchContext);
