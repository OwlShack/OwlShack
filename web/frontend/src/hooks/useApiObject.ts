import { useCallback, useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";

interface ApiObject<T> {
  /** null until the first successful load. */
  item: T | null;
  setItem: Dispatch<SetStateAction<T | null>>;
  loading: boolean;
  error: string | null;
  /** The last load answered 404, as against failing some other way. */
  notFound: boolean;
  reload: () => void;
}

// Single-object sibling of useApiList; pass url=null to defer.
export function useApiObject<T>(
  url: string | null,
  errorMessage: string,
): ApiObject<T> {
  const [item, setItem] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);

  const seq = useRef(0);
  const reload = useCallback(() => {
    if (!url) return;
    const id = ++seq.current;
    setLoading(true);
    setError(null);
    setNotFound(false);
    fetch(url)
      .then((r) => {
        if (r.status === 404 && seq.current === id) setNotFound(true);
        if (!r.ok) throw new Error(errorMessage);
        return r.json();
      })
      .then((data: T) => {
        if (seq.current === id) setItem(data);
      })
      .catch(() => {
        if (seq.current === id) setError(errorMessage);
      })
      .finally(() => {
        if (seq.current === id) setLoading(false);
      });
  }, [url, errorMessage]);

  useEffect(() => {
    reload();
    return () => {
      seq.current++;
    };
  }, [reload]);

  return { item, setItem, loading, error, notFound, reload };
}
